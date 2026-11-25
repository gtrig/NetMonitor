package debug

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/scheduler"
	"github.com/gtrig/netmon/internal/store"
)

const testToken = "0123456789abcdef-token"

type fakeSched struct{ calls []string }

func (f *fakeSched) ScanNow(n string) error         { f.calls = append(f.calls, n); return nil }
func (f *fakeSched) ScanJob(int) error              { return errors.New("no such job") }
func (f *fakeSched) ScanDevice(n, k string) error   { f.calls = append(f.calls, n+"|"+k); return nil }
func (f *fakeSched) Status() []scheduler.JobStatus  { return nil }
func (f *fakeSched) Sweeps() []scheduler.SweepState { return []scheduler.SweepState{{Network: "lan"}} }
func (f *fakeSched) Presence([]model.Device) map[model.MemberRef]model.Presence {
	return map[model.MemberRef]model.Presence{{Network: "lan", Key: "ip:192.168.8.9"}: {Asleep: true}}
}

type env struct {
	h     http.Handler
	cfg   *config.Config
	sched *fakeSched
	logs  *Logs
}

func setup(t *testing.T) *env {
	t.Helper()
	db := filepath.Join(t.TempDir(), "netmon.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	if err := st.UpsertDevices(context.Background(), []model.Device{{Network: "lan", Key: "ip:192.168.8.9",
		IP: netip.MustParseAddr("192.168.8.9"), Online: true, FirstSeen: now, LastSeen: now}}); err != nil {
		t.Fatal(err)
	}
	e := &env{sched: &fakeSched{}, logs: &Logs{}, cfg: &config.Config{
		Auth:     &config.BasicAuth{Username: "u", Password: `hunter"2`},
		Networks: []config.Network{{Name: "lan", CIDR: netip.MustParsePrefix("192.168.8.0/24")}}}}
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, _ := r.BasicAuth()
		io.WriteString(w, r.Method+" "+r.URL.String()+" "+pass)
	})
	e.h = New(Deps{Token: func() string { return testToken }, Logs: e.logs, Store: st, DBPath: db, UI: ui, Raw: func() *config.Config { return e.cfg },
		Runtime: func() Runtime { return Runtime{Config: e.cfg, Scheduler: e.sched} }})
	return e
}

func (e *env) post(from, body string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", Path, strings.NewReader(body))
	r.RemoteAddr = from
	r.Header.Set("Authorization", "Bearer "+testToken)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

// call runs a tool and returns its text and whether it failed.
func (e *env) call(t *testing.T, name, args string) (string, bool) {
	t.Helper()
	w := e.post("127.0.0.1:5", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
	var res struct {
		Result struct {
			Content []struct{ Text string }
			IsError bool
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || len(res.Result.Content) != 1 {
		t.Fatalf("%s: %v %s", name, err, w.Body)
	}
	return res.Result.Content[0].Text, res.Result.IsError
}

const pingReq = `{"jsonrpc":"2.0","id":1,"method":"ping"}`

func TestGuard(t *testing.T) {
	e := setup(t)
	e.logs = nil
	check := func(from string, want int, header ...string) {
		t.Helper()
		if got := e.post(from, pingReq, header...).Code; got != want {
			t.Errorf("%s %v: %d, want %d", from, header, got, want)
		}
	}
	check("127.0.0.1:5", 200)
	check("[::1]:5", 200)
	check("192.168.8.2:5", 403)
	check("192.168.8.2:5", 403, "X-Forwarded-For", "127.0.0.1")
	check("127.0.0.1:5", 401, "Authorization", "")
	check("127.0.0.1:5", 401, "Authorization", "Bearer wrong")
	check("127.0.0.1:5", 403, "Origin", "http://evil.example")
	// The list applies at once, and only it.
	e.cfg.Debug.Allow = []netip.Prefix{netip.MustParsePrefix("192.168.8.0/24")}
	check("192.168.8.2:5", 200)
	check("192.168.8.2:5", 401, "Authorization", "")
	check("127.0.0.1:5", 403)
}

func TestProtocol(t *testing.T) {
	e := setup(t)
	for body, want := range map[string]string{
		`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"2025-03-26"}}`: `"protocolVersion":"2025-03-26"`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`:   `"protocolVersion":"2025-11-25"`,
		pingReq: `"result":{}`,
		`{"jsonrpc":"2.0","id":1,"method":"nope"}`:                              `-32601`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rm"}}`: `-32602`,
		`{`:  `-32700`,
		`[]`: `-32600`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`: `"inputSchema"`,
	} {
		if got := e.post("127.0.0.1:5", body).Body.String(); !strings.Contains(got, want) {
			t.Errorf("%s: %s, want %s", body, got, want)
		}
	}
	if w := e.post("127.0.0.1:5", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); w.Code != 202 || w.Body.Len() != 0 {
		t.Errorf("notification: %d %s", w.Code, w.Body)
	}
}

func TestTools(t *testing.T) {
	e := setup(t)
	for _, c := range []struct{ name, args, want string }{
		{"status", `{}`, `"goroutines"`},
		{"devices", `{"state":"asleep","search":"8.9"}`, `192.168.8.9`},
		{"devices", `{"state":"offline"}`, `[]`},
		{"sweeps", `{"network":"lan"}`, `"network": "lan"`},
		{"events", `{}`, `[]`},
		{"job_runs", `{"outcome":"failed"}`, `[]`},
		{"config", `{}`, `"auth": true`},
		{"query", `{"sql":"SELECT key FROM devices;"}`, `192.168.8.9`},
		{"goroutines", `{}`, `goroutine profile`},
		{"get", `{"path":"/api/x?y=1"}`, "200 OK\nContent-Type: \n\nGET http://netmon/api/x?y=1 "},
		{"scan", `{"network":"lan","key":"k"}`, "started"},
	} {
		if got, failed := e.call(t, c.name, c.args); failed || !strings.Contains(got, c.want) {
			t.Errorf("%s %s: %v %s", c.name, c.args, failed, got)
		}
	}
	if len(e.sched.calls) != 1 || e.sched.calls[0] != "lan|k" {
		t.Error(e.sched.calls)
	}
	for _, c := range [][2]string{
		{"status", `{"x":1}`}, {"devices", `{"state":"up"}`}, {"logs", `{"level":"loud"}`}, {"get", `{"path":"http://evil/"}`},
		{"scan", `{}`}, {"scan", `{"job":7}`}, {"scan", `{"job":0,"network":"lan"}`},
		{"ping", `{"targets":["8.8.8.8"]}`}, {"ping", `{"targets":["192.168.0.0/16"]}`}, {"ping", `{"targets":[]}`}, {"ping", `{"targets":["192.168.8.9"],"count":11}`},
		{"query", `{"sql":"DELETE FROM devices"}`}, {"query", `{"sql":"PRAGMA query_only=0"}`}, {"query", `{"sql":"ATTACH 'x' AS y"}`},
		{"query", `{"sql":"SELECT 1; DELETE FROM devices"}`}, {"query", `{"sql":"WITH x AS (SELECT 1) DELETE FROM devices"}`},
	} {
		if got, failed := e.call(t, c[0], c[1]); !failed {
			t.Errorf("%s %s accepted: %s", c[0], c[1], got)
		}
	}
	if got, _ := e.call(t, "query", `{"sql":"SELECT COUNT(*) FROM devices"}`); !strings.Contains(got, "1") {
		t.Error("devices were deleted:", got)
	}
}

func TestQueryLimits(t *testing.T) {
	e := setup(t)
	got, _ := e.call(t, "query", `{"sql":"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n) SELECT i FROM n"}`)
	if !strings.Contains(got, `"more": true`) {
		t.Error(got[:min(len(got), 200)])
	}
	start := time.Now()
	if _, failed := e.call(t, "query", `{"sql":"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n) SELECT COUNT(*) FROM n"}`); !failed || time.Since(start) > 2*queryTimeout {
		t.Error("endless query:", failed, time.Since(start))
	}
}

func TestSecretsAreHidden(t *testing.T) {
	e := setup(t)
	e.logs.Start()
	log := slog.New(e.logs.Handler(slog.DiscardHandler))
	log.Warn(`login failed`, "pass", `hunter"2`, "token", testToken)
	for _, c := range [][2]string{{"logs", `{}`}, {"get", `{"path":"/"}`}, {"query", `{"sql":"SELECT 'hunter\"2'"}`}} {
		got, _ := e.call(t, c[0], c[1])
		if strings.Contains(got, "hunter") || strings.Contains(got, testToken) || !strings.Contains(got, "••••••") {
			t.Errorf("%s: %s", c[0], got)
		}
	}
}

func TestLogs(t *testing.T) {
	l := &Logs{}
	var b strings.Builder
	log := slog.New(l.Handler(slog.NewTextHandler(&b, nil))).With("a", 1)
	log.Debug("off")
	l.Start()
	log.Debug("quiet")
	log.WithGroup("g").Warn("loud", "x", 2)
	if strings.Contains(b.String(), "quiet") || !strings.Contains(b.String(), "loud") {
		t.Error("stderr:", b.String())
	}
	got := l.Select(0, slog.LevelDebug, "", 10)
	if len(got) != 2 || got[0].Line != "msg=quiet a=1" || got[1].Line != "msg=loud a=1 g.x=2" || got[1].Level != "WARN" {
		t.Errorf("%+v", got)
	}
	if got := l.Select(1, slog.LevelDebug, "LOUD", 10); len(got) != 1 {
		t.Error(got)
	}
	if got := l.Select(0, slog.LevelError, "", 10); len(got) != 0 {
		t.Error(got)
	}
	for range 3 * maxLogs {
		log.Debug("x")
	}
	if n := len(l.entries); n < maxLogs || n > 2*maxLogs {
		t.Error(n)
	}
	l.Stop()
	log.Warn("after")
	if len(l.entries) != 0 {
		t.Error(l.entries)
	}
}

func TestManager(t *testing.T) {
	e := setup(t)
	path := filepath.Join(t.TempDir(), "token")
	m := NewManager(Options{TokenPath: path}, Deps{Logs: e.logs, Runtime: func() Runtime { return Runtime{Config: e.cfg, Scheduler: e.sched} }})
	t.Cleanup(m.Close)
	ask := func(addr, token string) int {
		t.Helper()
		r, _ := http.NewRequest("POST", "http://"+addr+Path, strings.NewReader(pingReq))
		r.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			return 0
		}
		res.Body.Close()
		return res.StatusCode
	}
	m.Apply(config.Debug{})
	if m.Status().Running || m.Status().Error != "" {
		t.Fatal(m.Status())
	}
	on := config.Debug{Enabled: true, Listen: "127.0.0.1:0"}
	m.Apply(on)
	first := m.Status().Addr
	b, _ := os.ReadFile(path)
	old := strings.TrimSpace(string(b))
	if ask(first, old) != 200 {
		t.Fatal(m.Status())
	}
	m.Apply(on)
	if m.Status().Addr != first {
		t.Error("restarted without need")
	}
	token, err := m.NewToken()
	if err != nil || ask(first, old) != 401 || ask(first, token) != 200 {
		t.Error("the new token does not replace the old one", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Error(fi.Mode())
	}
	// Another address.
	m.Apply(config.Debug{Enabled: true, Listen: "localhost:0"})
	if second := m.Status().Addr; second == first || ask(first, token) != 0 || ask(second, token) != 200 {
		t.Error("did not move", first, second)
	}
	// A taken address shows, and a later good one recovers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	m.Apply(config.Debug{Enabled: true, Listen: ln.Addr().String()})
	if st := m.Status(); st.Running || st.Error == "" {
		t.Error(st)
	}
	m.Apply(on)
	addr := m.Status().Addr
	if st := m.Status(); !st.Running || st.Error != "" {
		t.Error(st)
	}
	m.Apply(config.Debug{})
	if m.Status().Running || ask(addr, token) != 0 {
		t.Error("still running")
	}
	forced := NewManager(Options{Forced: true, Token: testToken}, m.deps)
	t.Cleanup(forced.Close)
	forced.Apply(config.Debug{Listen: "127.0.0.1:0"})
	if st := forced.Status(); !st.Running || !st.TokenGiven || ask(st.Addr, testToken) != 200 {
		t.Error(st)
	}
	if _, err := forced.NewToken(); !errors.Is(err, ErrTokenGiven) {
		t.Error(err)
	}
}

func TestLoadToken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	a, _, err := LoadToken("", p)
	if err != nil || len(a) < minToken {
		t.Fatal(a, err)
	}
	if b, _, _ := LoadToken("", p); b != a {
		t.Error("the token changed")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Error(fi.Mode())
	}
	if _, _, err := LoadToken("short", p); err == nil {
		t.Error("short token accepted")
	}
}
