package debug

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	rdebug "runtime/debug"
	"runtime/pprof"
	"slices"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/settings"
	"github.com/gtrig/netmon/internal/store"
	"github.com/gtrig/netmon/pinger"
)

const (
	maxRows      = 500
	maxPings     = 1024
	maxPingCount = 10
	queryTimeout = 5 * time.Second
)

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`

	run func(ctx context.Context, args json.RawMessage) (any, error)
}

// prop is one argument: name, JSON type and description; required names end in "!".
type prop struct{ name, typ, desc string }

func newTool(name, desc string, readOnly bool, run func(context.Context, json.RawMessage) (any, error), props ...prop) tool {
	ps, required := map[string]any{}, []string{}
	for _, p := range props {
		n, must := strings.CutSuffix(p.name, "!")
		if must {
			required = append(required, n)
		}
		schema := map[string]any{"type": p.typ, "description": p.desc}
		if p.typ == "array" {
			schema["items"] = map[string]string{"type": "string"}
		}
		ps[n] = schema
	}
	return tool{name, desc, map[string]any{"type": "object", "properties": ps, "required": required, "additionalProperties": false},
		map[string]any{"readOnlyHint": readOnly, "destructiveHint": false}, run}
}

func (s *server) allTools() []tool {
	network := prop{"network", "string", "Only this network"}
	limit := prop{"limit", "integer", "At most this many (default 100, up to 500)"}
	before := prop{"before", "integer", "Only those with a lower id, for paging"}
	return []tool{
		newTool("status", "Build, uptime, memory, networks, and the state of every job and queued scan.", true, s.status),
		newTool("devices", "Stored devices with how ping judges them (learned pattern, silence limit in seconds, asleep).", true, s.devices,
			network, prop{"search", "string", "Text in the key, address, name, label or vendor"}, prop{"state", "string", "online, offline or asleep"}),
		newTool("sweeps", "In-memory state of the ping sweeps: misses, silence, held offline marking and learned gaps per device.", true, s.sweeps,
			network, prop{"key", "string", "Only this device key"}),
		newTool("events", "Detected changes, newest first.", true, s.events, network, limit, before),
		newTool("job_runs", "History of scans, newest first.", true, s.jobRuns, network, prop{"kind", "string", "presence, details, device or ping"},
			prop{"outcome", "string", "ok, failed, cancelled, skipped or interrupted"}, limit, before),
		newTool("logs", "Latest log records (including debug ones), oldest first.", true, s.logs, prop{"level", "string", "Minimum: debug (default), info, warn or error"},
			prop{"contains", "string", "Only lines with this text"}, prop{"after", "integer", "Only records with a higher seq"}, limit),
		newTool("config", "The config file as loaded, with secrets masked.", true, s.config),
		newTool("query", "Runs one read-only SELECT on the SQLite database. Times are Unix seconds. SELECT name, sql FROM sqlite_master shows the tables.", true, s.query,
			prop{"sql!", "string", "A single SELECT or WITH statement"}),
		newTool("get", "Fetches a page or JSON API path of the web UI, such as /api/graph.", true, s.get, prop{"path!", "string", "Path with optional query string"}),
		newTool("goroutines", "Stacks of all goroutines.", true, s.goroutines, prop{"full", "boolean", "One by one with wait times, instead of grouped"}),
		newTool("ping", "Pings addresses inside the configured networks from netmon's host and returns their times in milliseconds.", false, s.ping,
			prop{"targets!", "array", "Addresses or networks, such as 192.168.8.12 or 192.168.8.0/24"}, prop{"count", "integer", "Pings per address (default 3, up to 10)"}),
		newTool("scan", "Starts a scan without waiting for it: a job (id from status), one device in detail (network and key), or a network's presence.", false, s.scan,
			prop{"job", "integer", "Job id"}, network, prop{"key", "string", "Device key"}),
	}
}

func rows(limit int) int {
	if limit <= 0 {
		return 100
	}
	return min(limit, maxRows)
}

func (s *server) status(ctx context.Context, args json.RawMessage) (any, error) {
	if err := decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	rt := s.Runtime()
	devs, err := s.Store.Devices(ctx, "")
	if err != nil {
		return nil, err
	}
	type network struct {
		Name   string `json:"name"`
		CIDR   string `json:"cidr"`
		Online int    `json:"online"`
		Total  int    `json:"total"`
	}
	nets := []network{}
	for _, n := range rt.Config.Networks {
		x := network{Name: n.Name, CIDR: n.CIDR.String()}
		for _, d := range devs {
			if d.Network == n.Name {
				x.Total++
				if d.Online {
					x.Online++
				}
			}
		}
		nets = append(nets, x)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	build := map[string]string{"go": runtime.Version()}
	if bi, ok := rdebug.ReadBuildInfo(); ok {
		for _, st := range bi.Settings {
			if strings.HasPrefix(st.Key, "vcs.") {
				build[st.Key] = st.Value
			}
		}
	}
	return map[string]any{"build": build, "startedAt": s.started.UTC(), "now": time.Now().UTC(), "goroutines": runtime.NumGoroutine(),
		"heapBytes": m.HeapAlloc, "config": s.ConfigPath, "listen": rt.Config.Listen, "database": s.DBPath,
		"networks": nets, "jobs": rt.Scheduler.Status()}, nil
}

func (s *server) devices(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct{ Network, Search, State string }
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if !slices.Contains([]string{"", "online", "offline", "asleep"}, a.State) {
		return nil, errors.New("state must be online, offline or asleep")
	}
	devs, err := s.Store.Devices(ctx, a.Network)
	if err != nil {
		return nil, err
	}
	type pingJSON struct {
		Pattern string  `json:"pattern"`
		Typical float64 `json:"typicalSilence,omitempty"`
		Limit   float64 `json:"offlineAfter"`
	}
	type device struct {
		Network   string               `json:"network"`
		Key       string               `json:"key"`
		IP        string               `json:"ip,omitempty"`
		Hostname  string               `json:"hostname,omitempty"`
		Label     string               `json:"label,omitempty"`
		Vendor    string               `json:"vendor,omitempty"`
		State     string               `json:"state"`
		Known     bool                 `json:"known"`
		FirstSeen time.Time            `json:"firstSeen"`
		LastSeen  time.Time            `json:"lastSeen"`
		SeenBy    map[string]time.Time `json:"seenBy"`
		Override  float64              `json:"offlineAfterOverride,omitempty"`
		Ping      *pingJSON            `json:"ping,omitempty"` // nil: not judged by ping
	}
	presence := s.Runtime().Scheduler.Presence(devs)
	out := []device{}
	for _, d := range devs {
		x := device{Network: d.Network, Key: d.Key, Hostname: d.Hostname, Label: d.Label, Vendor: d.Vendor, State: "offline", Known: d.Known,
			FirstSeen: d.FirstSeen, LastSeen: d.LastSeen, SeenBy: d.SeenBy, Override: d.OfflineAfter.Seconds()}
		if d.IP.IsValid() {
			x.IP = d.IP.String()
		}
		if d.Online {
			x.State = "online"
		}
		if p, ok := presence[model.MemberRef{Network: d.Network, Key: d.Key}]; ok {
			x.Ping = &pingJSON{"none", p.Pattern.Typical.Seconds(), p.Limit.Seconds()}
			if p.Pattern.Periodic {
				x.Ping.Pattern = "periodic"
			}
			if p.Asleep {
				x.State = "asleep"
			}
		}
		text := strings.ToLower(strings.Join([]string{x.Key, x.IP, x.Hostname, x.Label, x.Vendor}, "\n"))
		if (a.State == "" || a.State == x.State) && strings.Contains(text, strings.ToLower(a.Search)) {
			out = append(out, x)
		}
	}
	return out, nil
}

func (s *server) sweeps(_ context.Context, args json.RawMessage) (any, error) {
	var a struct{ Network, Key string }
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	sws := s.Runtime().Scheduler.Sweeps()
	out := sws[:0]
	for _, sw := range sws {
		if a.Network != "" && sw.Network != a.Network {
			continue
		}
		if a.Key != "" {
			kept := sw.Hosts[:0]
			for _, h := range sw.Hosts {
				if h.Key == a.Key {
					kept = append(kept, h)
				}
			}
			sw.Hosts = kept
		}
		out = append(out, sw)
	}
	return out, nil
}

func (s *server) events(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Network string
		Limit   int
		Before  int64
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	evs, err := s.Store.Events(ctx, store.EventFilter{Network: a.Network, Limit: rows(a.Limit), Before: a.Before})
	if evs == nil {
		evs = []model.Event{}
	}
	return evs, err
}

func (s *server) jobRuns(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Network, Kind, Outcome string
		Limit                  int
		Before                 int64
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	runs, err := s.Store.JobRuns(ctx, store.JobRunFilter{Network: a.Network, Kind: a.Kind, Outcome: a.Outcome, Limit: rows(a.Limit), Before: a.Before})
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, r := range runs {
		out = append(out, struct {
			model.JobRun
			Duration string
		}{r, r.Duration.String()})
	}
	return out, nil
}

func (s *server) logs(_ context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Level, Contains string
		After           int64
		Limit           int
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	level := slog.LevelDebug
	if a.Level != "" {
		if err := level.UnmarshalText([]byte(a.Level)); err != nil {
			return nil, errors.New("level must be debug, info, warn or error")
		}
	}
	return s.Logs.Select(a.After, level, a.Contains, rows(a.Limit)), nil
}

func (s *server) config(_ context.Context, args json.RawMessage) (any, error) {
	if err := decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	raw := s.Raw()
	return map[string]any{"path": s.ConfigPath, "listen": raw.Listen, "data_dir": raw.DataDir, "auth": raw.Auth != nil,
		"debug": map[string]any{"listen": raw.Debug.Addr(), "allow": raw.Debug.Networks()}, "settings": settings.Mask(raw)}, nil
}

var selects = regexp.MustCompile(`(?is)^\s*(select|with)\b`)

func (s *server) query(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct{ SQL string }
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	q := strings.TrimSuffix(strings.TrimSpace(a.SQL), ";")
	// The connection cannot write; this also keeps ATTACH and PRAGMA out.
	if !selects.MatchString(q) || strings.Contains(q, ";") {
		return nil, errors.New("only a single SELECT or WITH statement without semicolons is allowed")
	}
	path, err := filepath.Abs(s.DBPath)
	if err != nil {
		return nil, err
	}
	// A connection of its own: the store has only one, and a slow query must not block netmon.
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rs, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	out, size, more := [][]any{}, 0, false
	for rs.Next() {
		if len(out) == maxRows || size > maxText {
			more = true
			break
		}
		vals, ptrs := make([]any, len(cols)), make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			switch v := v.(type) {
			case []byte:
				vals[i] = string(v)
				size += len(v)
			case string:
				size += len(v)
			}
		}
		out = append(out, vals)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"columns": cols, "rows": out, "more": more}, nil
}

// recorder keeps a response in memory.
type recorder struct {
	code int
	head http.Header
	body bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.head }
func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(b)
}

func (s *server) get(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct{ Path string }
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(a.Path, "/") {
		return nil, errors.New("path must start with /")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://netmon"+a.Path, nil)
	if err != nil {
		return nil, err
	}
	if auth := s.Runtime().Config.Auth; auth != nil {
		req.SetBasicAuth(auth.Username, auth.Password)
	}
	rec := &recorder{head: http.Header{}}
	s.UI.ServeHTTP(rec, req)
	return fmt.Sprintf("%d %s\nContent-Type: %s\n\n%s", rec.code, http.StatusText(rec.code), rec.head.Get("Content-Type"), rec.body.String()), nil
}

func (s *server) goroutines(_ context.Context, args json.RawMessage) (any, error) {
	var a struct{ Full bool }
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	level := 1
	if a.Full {
		level = 2
	}
	var b bytes.Buffer
	err := pprof.Lookup("goroutine").WriteTo(&b, level)
	return b.String(), err
}

func (s *server) ping(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Targets []string
		Count   int
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if a.Count == 0 {
		a.Count = pinger.DefaultCount
	}
	if a.Count < 1 || a.Count > maxPingCount {
		return nil, fmt.Errorf("count must be 1 to %d", maxPingCount)
	}
	nets := s.Runtime().Config.Networks
	seen := map[netip.Addr]bool{}
	var addrs []netip.Addr
	for _, t := range a.Targets {
		p, err := netip.ParsePrefix(t)
		if err != nil {
			addr, err := netip.ParseAddr(t)
			if err != nil {
				return nil, fmt.Errorf("%q is not an address or network", t)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		hosts, err := pinger.Hosts(p)
		if err != nil {
			return nil, err
		}
		for _, h := range hosts {
			if !slices.ContainsFunc(nets, func(n config.Network) bool { return n.CIDR.Contains(h) }) {
				return nil, fmt.Errorf("%s is not in a configured network", h)
			}
			excluded := slices.ContainsFunc(nets, func(n config.Network) bool { return slices.Contains(n.Exclude, h) })
			if !seen[h] && !excluded {
				seen[h] = true
				addrs = append(addrs, h)
			}
		}
	}
	if len(addrs) == 0 || len(addrs) > maxPings {
		return nil, fmt.Errorf("give 1 to %d addresses that are not excluded", maxPings)
	}
	found, err := pinger.Sweep(ctx, addrs, pinger.Options{Count: a.Count})
	if err != nil {
		return nil, err
	}
	type reply struct {
		IP       string  `json:"ip"`
		Sent     int     `json:"sent"`
		Received int     `json:"received"`
		Min      float64 `json:"minMs"`
		Avg      float64 `json:"avgMs"`
		Max      float64 `json:"maxMs"`
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	active := []reply{}
	for _, h := range addrs {
		if st, ok := found[h]; ok {
			active = append(active, reply{h.String(), st.Sent, st.Received, ms(st.Min), ms(st.Avg), ms(st.Max)})
		}
	}
	return map[string]any{"pinged": len(addrs), "active": active}, nil
}

func (s *server) scan(_ context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Job          *int
		Network, Key string
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	sched := s.Runtime().Scheduler
	var err error
	switch {
	case a.Job != nil && a.Network == "" && a.Key == "":
		err = sched.ScanJob(*a.Job)
	case a.Job == nil && a.Network != "" && a.Key != "":
		err = sched.ScanDevice(a.Network, a.Key)
	case a.Job == nil && a.Network != "":
		err = sched.ScanNow(a.Network)
	default:
		err = errors.New("give either job, or network, or network and key")
	}
	if err != nil {
		return nil, err
	}
	return "started; see status and job_runs", nil
}
