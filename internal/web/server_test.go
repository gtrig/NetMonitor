package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/notify"
	"github.com/gtrig/netmon/internal/scheduler"
	"github.com/gtrig/netmon/internal/store"
)

type fakeSched struct{ scanned []string }

func (f *fakeSched) ScanNow(n string) error {
	if n != "lan" {
		return scheduler.ErrUnknownNetwork
	}
	f.scanned = append(f.scanned, n)
	return nil
}
func (f *fakeSched) ScanJob(id int) error {
	if id != 0 {
		return scheduler.ErrUnknownJob
	}
	f.scanned = append(f.scanned, "job0")
	return nil
}
func (f *fakeSched) ScanDevice(network, key string) error {
	if key != "mac:aa:00:00:00:00:01" {
		return scheduler.ErrUnknownDevice
	}
	f.scanned = append(f.scanned, network+"|"+key)
	return nil
}
func (f *fakeSched) Status() []scheduler.JobStatus {
	return []scheduler.JobStatus{{Network: "lan", Source: "nmap"}}
}

type fakeNotifier struct{}

func (fakeNotifier) Test(ctx context.Context, ch string) error {
	if ch != "tg" {
		return notify.ErrUnknownChannel
	}
	return nil
}

func newTest(t *testing.T, auth *config.BasicAuth) (http.Handler, *store.Store, *fakeSched) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	st.UpsertDevices(context.Background(), []model.Device{{Key: "mac:aa:00:00:00:00:01", Network: "lan",
		MAC: "aa:00:00:00:00:01", IP: netip.MustParseAddr("10.0.0.1"), Hostname: "nas", Online: true,
		FirstSeen: now, LastSeen: now, Sources: []string{"nmap"}}})
	st.AddEvents(context.Background(), []model.Event{{Type: model.EventBaseline, Network: "lan", Message: "Baseline: 1 devices discovered", At: now}})
	cfg := &config.Config{Auth: auth, Networks: []config.Network{{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24")}},
		Channels: []config.Channel{{Name: "tg", Type: "telegram"}}}
	fs := &fakeSched{}
	return New(Deps{Store: st, Runtime: func() Runtime {
		return Runtime{Scheduler: fs, Notifier: fakeNotifier{}, Config: cfg}
	}, Settings: &fakeSettings{raw: &config.Config{}}}), st, fs
}

func do(h http.Handler, method, path, body string, auth bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		req.SetBasicAuth("admin", "secret")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

var creds = &config.BasicAuth{Username: "admin", Password: "secret"}

func TestHealthzNoAuth(t *testing.T) {
	h, _, _ := newTest(t, creds)
	rr := do(h, "GET", "/healthz", "", false)
	if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != "ok" {
		t.Fatalf("%d %q", rr.Code, rr.Body)
	}
}

func TestAuthRequired(t *testing.T) {
	h, _, _ := newTest(t, creds)
	if rr := do(h, "GET", "/api/devices", "", false); rr.Code != 401 || rr.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no creds: %d", rr.Code)
	}
	if rr := do(h, "GET", "/api/devices", "", true); rr.Code != 200 {
		t.Fatalf("with creds: %d", rr.Code)
	}
}

func TestPatchDevice(t *testing.T) {
	h, _, _ := newTest(t, nil)
	rr := do(h, "PATCH", "/api/devices/mac:aa:00:00:00:00:01", `{"label":"My NAS","known":true}`, false)
	if rr.Code != 204 {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body)
	}
	rr = do(h, "GET", "/api/devices?network=lan", "", false)
	var devs []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &devs)
	if len(devs) != 1 || devs[0]["label"] != "My NAS" || devs[0]["known"] != true || devs[0]["ip"] != "10.0.0.1" {
		t.Fatalf("devices = %s", rr.Body)
	}
}

func TestPatchUnknown404(t *testing.T) {
	h, _, _ := newTest(t, nil)
	if rr := do(h, "PATCH", "/api/devices/mac:nope", `{"known":true}`, false); rr.Code != 404 {
		t.Fatalf("%d", rr.Code)
	}
	if rr := do(h, "PATCH", "/api/devices/mac:aa:00:00:00:00:01", `not json`, false); rr.Code != 400 {
		t.Fatalf("bad json: %d", rr.Code)
	}
}

func TestScanNow202(t *testing.T) {
	h, _, fs := newTest(t, nil)
	if rr := do(h, "POST", "/api/scan/lan", "", false); rr.Code != 202 || len(fs.scanned) != 1 {
		t.Fatalf("%d", rr.Code)
	}
	if rr := do(h, "POST", "/api/scan/nope", "", false); rr.Code != 404 {
		t.Fatalf("%d", rr.Code)
	}
}

func TestNotifyTest(t *testing.T) {
	h, _, _ := newTest(t, nil)
	if rr := do(h, "POST", "/api/notify/test/tg", "", false); rr.Code != 204 {
		t.Fatalf("%d", rr.Code)
	}
	if rr := do(h, "POST", "/api/notify/test/nope", "", false); rr.Code != 404 {
		t.Fatalf("%d", rr.Code)
	}
}

func TestGraphJSON(t *testing.T) {
	h, _, _ := newTest(t, nil)
	rr := do(h, "GET", "/api/graph?network=lan", "", false)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"group":"nodes"`) {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
}

func TestEventsAndNetworksJSON(t *testing.T) {
	h, _, _ := newTest(t, nil)
	rr := do(h, "GET", "/api/events?limit=5", "", false)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Baseline") {
		t.Fatalf("events: %d %s", rr.Code, rr.Body)
	}
	rr = do(h, "GET", "/api/networks", "", false)
	var nets []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &nets)
	if len(nets) != 1 || nets[0]["online"].(float64) != 1 || nets[0]["total"].(float64) != 1 {
		t.Fatalf("networks: %s", rr.Body)
	}
}

func TestPagesRender(t *testing.T) {
	h, _, _ := newTest(t, nil)
	for path, h1 := range map[string]string{"/": "Overview", "/map": "Network map", "/devices": "Devices", "/events": "Events"} {
		rr := do(h, "GET", path, "", false)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), "<h1>"+h1+"</h1>") {
			t.Errorf("%s: %d", path, rr.Code)
		}
	}
	if rr := do(h, "GET", "/static/cytoscape.min.js", "", false); rr.Code != 200 {
		t.Errorf("static: %d", rr.Code)
	}
}

// Finding 6: cross-site requests to mutating endpoints are rejected.
func TestCrossOriginPostRejected(t *testing.T) {
	h, _, fs := newTest(t, nil)
	req := httptest.NewRequest("POST", "/api/scan/lan", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 403 || len(fs.scanned) != 0 {
		t.Fatalf("code=%d scanned=%v", rr.Code, fs.scanned)
	}
}

func TestJobsAPI(t *testing.T) {
	h, _, fs := newTest(t, creds)
	rr := do(h, "GET", "/api/jobs", "", true)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"source":"nmap"`) {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if rr := do(h, "POST", "/api/jobs/0/scan", "", true); rr.Code != 202 || len(fs.scanned) != 1 {
		t.Fatalf("scan: %d %v", rr.Code, fs.scanned)
	}
	if rr := do(h, "POST", "/api/jobs/0/scan?network=other", "", true); rr.Code != 404 {
		t.Fatalf("stale job id: %d", rr.Code)
	}
	for _, id := range []string{"9", "x"} {
		if rr := do(h, "POST", "/api/jobs/"+id+"/scan", "", true); rr.Code != 404 {
			t.Fatalf("job %s: %d", id, rr.Code)
		}
	}
}

func TestDetailsAPI(t *testing.T) {
	h, st, fs := newTest(t, creds)
	st.SaveDetails(context.Background(), []model.DeviceDetails{{Network: "lan", Key: "mac:aa:00:00:00:00:01", ScannedAt: time.Now(),
		OS: "Linux", Ports: []model.Port{{Proto: "tcp", Port: 22, State: "open", Service: "ssh", Product: "OpenSSH"}}}})
	rr := do(h, "GET", "/api/details?network=lan", "", true)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"product":"OpenSSH"`) {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if rr := do(h, "POST", "/api/details/scan", `{"network":"lan","key":"mac:aa:00:00:00:00:01"}`, true); rr.Code != 202 || len(fs.scanned) != 1 {
		t.Fatalf("scan: %d %v", rr.Code, fs.scanned)
	}
	if rr := do(h, "POST", "/api/details/scan", `{"network":"lan","key":"nope"}`, true); rr.Code != 404 {
		t.Fatalf("unknown: %d", rr.Code)
	}
	if rr := do(h, "GET", "/devices", "", true); !strings.Contains(rr.Body.String(), "22 ssh") {
		t.Fatalf("devices page lacks services summary")
	}
}

func TestAPIGraphIncludesRTT(t *testing.T) {
	h, st, _ := newTest(t, nil)
	ctx := context.Background()
	devs, _ := st.Devices(ctx, "lan")
	if len(devs) == 0 {
		t.Fatal("no seeded device")
	}
	sample := []model.PingSample{{Key: devs[0].Key, RTT: 2500 * time.Microsecond}}
	// "other" is not where lan is measured from now, so its times are left out.
	if err := st.RecordPings(ctx, "lan", "other", time.Now(), sample); err != nil {
		t.Fatal(err)
	}
	if body := do(h, "GET", "/api/graph?network=lan", "", false).Body.String(); strings.Contains(body, `"rtt"`) {
		t.Fatalf("times of another origin shown: %s", body)
	}
	st.RecordPings(ctx, "lan", "netmon", time.Now(), sample)
	if body := do(h, "GET", "/api/graph?network=lan", "", false).Body.String(); !strings.Contains(body, `"ms":2.5`) {
		t.Fatalf("no time in %s", body)
	}
}

func TestMapPageControls(t *testing.T) {
	h, _, _ := newTest(t, nil)
	body := do(h, "GET", "/map", "", false).Body.String()
	for _, v := range []string{"radar", "sectors", "lanes"} {
		if !strings.Contains(body, `<option value="`+v+`">`) {
			t.Errorf("no layout %s", v)
		}
	}
	for _, o := range []string{"names", "ips", "ms", "ports", "rings", "lines", "offline", "distance"} {
		if !strings.Contains(body, `data-opt="`+o+`"`) {
			t.Errorf("no option %s", o)
		}
	}
}
