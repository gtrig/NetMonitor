// Package web serves the netmon UI and JSON API.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/notify"
	"github.com/gtrig/netmon/internal/scheduler"
	"github.com/gtrig/netmon/internal/settings"
	"github.com/gtrig/netmon/internal/store"
	"github.com/gtrig/netmon/internal/topology"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Runtime is the currently running configuration and components; it changes on hot reload.
type Runtime struct {
	Scheduler interface {
		ScanNow(network string) error
		ScanJob(id int) error
		ScanDevice(network, key string) error
		Status() []scheduler.JobStatus
	}
	Notifier interface {
		Test(ctx context.Context, channel string) error
	}
	Config   *config.Config    // effective config
	Gateways map[string]string // network → router ID for unlinked devices
}

// Settings reads and writes the raw config file.
type Settings interface {
	Raw() *config.Config
	Save(raw *config.Config, renames map[string]string) error
	Reload() error
	Path() string
}

// Deps are the server's collaborators.
type Deps struct {
	Store    *store.Store
	Runtime  func() Runtime
	Settings Settings
}

type server struct {
	Deps
	pages map[string]*template.Template
}

func (s *server) rt() Runtime { return s.Runtime() }

var funcs = template.FuncMap{
	"ago": func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		d := time.Since(t).Round(time.Second)
		switch {
		case d < time.Minute:
			return strconv.Itoa(int(d.Seconds())) + "s ago"
		case d < time.Hour:
			return strconv.Itoa(int(d.Minutes())) + "m ago"
		case d < 48*time.Hour:
			return strconv.Itoa(int(d.Hours())) + "h ago"
		}
		return t.Local().Format("2006-01-02")
	},
	"ts": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
}

// New returns the HTTP handler.
func New(d Deps) http.Handler {
	s := &server{Deps: d, pages: map[string]*template.Template{}}
	for _, p := range []string{"dashboard", "map", "devices", "device", "events", "settings"} {
		s.pages[p] = template.Must(template.New("layout.html").Funcs(funcs).
			ParseFS(templateFS, "templates/layout.html", "templates/"+p+".html"))
	}
	static, _ := fs.Sub(staticFS, "static")

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /map", s.page("map", "Network map"))
	mux.HandleFunc("GET /devices", s.devicesPage)
	mux.HandleFunc("GET /device", s.devicePage)
	mux.HandleFunc("GET /events", s.eventsPage)
	mux.HandleFunc("GET /api/networks", s.apiNetworks)
	mux.HandleFunc("GET /api/graph", s.apiGraph)
	mux.HandleFunc("GET /api/devices", s.apiDevices)
	mux.HandleFunc("GET /api/events", s.apiEvents)
	mux.HandleFunc("PATCH /api/devices/{key}", s.apiPatchDevice)
	mux.HandleFunc("DELETE /api/devices/{key}", s.apiDeleteDevice)
	mux.HandleFunc("POST /api/machines", s.apiMergeMachine)
	mux.HandleFunc("POST /api/machines/split", s.apiSplitMachine)
	mux.HandleFunc("GET /api/data/networks", s.apiDataNetworks)
	mux.HandleFunc("POST /api/data/networks/{name}/merge", s.apiMergeNetworkData)
	mux.HandleFunc("DELETE /api/data/networks/{name}", s.apiDeleteNetworkData)
	mux.HandleFunc("POST /api/scan/{network}", s.apiScan)
	mux.HandleFunc("GET /api/jobs", s.apiJobs)
	mux.HandleFunc("GET /api/details", s.apiDetails)
	mux.HandleFunc("POST /api/details/scan", s.apiScanDetails)
	mux.HandleFunc("POST /api/jobs/{id}/scan", s.apiScanJob)
	mux.HandleFunc("POST /api/notify/test/{channel}", s.apiNotifyTest)
	mux.HandleFunc("GET /settings", s.page("settings", "Settings"))
	mux.HandleFunc("GET /api/settings", s.apiGetSettings)
	mux.HandleFunc("PUT /api/settings", s.apiPutSettings)
	mux.HandleFunc("POST /api/settings/reload", s.apiReloadSettings)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", s.healthz)
	// Rejects cross-site POST/PATCH (CSRF, also against DNS-rebinding pages in browsers).
	root.Handle("/", s.auth(http.NewCrossOriginProtection().Handler(mux)))
	return securityHeaders(root)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

func (s *server) auth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := s.rt().Config.Auth // read per request: auth may change on reload
		if a == nil {
			h.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte(a.Username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(p), []byte(a.Password)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="netmon", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

// ---- JSON API ----

type deviceJSON struct {
	Key       string    `json:"key"`
	Network   string    `json:"network"`
	MAC       string    `json:"mac"`
	IP        string    `json:"ip"`
	Hostname  string    `json:"hostname"`
	Vendor    string    `json:"vendor"`
	Label     string    `json:"label"`
	Port      string    `json:"port"`
	Known     bool      `json:"known"`
	Online    bool      `json:"online"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Sources   []string  `json:"sources"`
	Machine   int64     `json:"machine,omitempty"`
}

func toJSON(d model.Device, machine int64) deviceJSON {
	ip := ""
	if d.IP.IsValid() {
		ip = d.IP.String()
	}
	if d.Sources == nil {
		d.Sources = []string{}
	}
	return deviceJSON{d.Key, d.Network, d.MAC, ip, d.Hostname, d.Vendor, d.Label, d.Port, d.Known, d.Online,
		d.FirstSeen, d.LastSeen, d.Sources, machine}
}

type eventJSON struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	Network   string    `json:"network"`
	DeviceKey string    `json:"deviceKey,omitempty"`
	Message   string    `json:"message"`
	Old       string    `json:"old,omitempty"`
	New       string    `json:"new,omitempty"`
	At        time.Time `json:"at"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("web: encode", "err", err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	slog.Error("web", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

type networkSummary struct {
	Name    string                `json:"name"`
	CIDR    string                `json:"cidr"`
	Online  int                   `json:"online"`
	Total   int                   `json:"total"`
	Unknown int                   `json:"unknown"`
	Jobs    []scheduler.JobStatus `json:"jobs"`
}

func (s *server) networks(ctx context.Context) ([]networkSummary, error) {
	devs, err := s.Store.Devices(ctx, "")
	if err != nil {
		return nil, err
	}
	rt := s.rt()
	status := rt.Scheduler.Status()
	var out []networkSummary
	for _, n := range rt.Config.Networks {
		ns := networkSummary{Name: n.Name, CIDR: n.CIDR.String(), Jobs: []scheduler.JobStatus{}}
		for _, d := range devs {
			if d.Network != n.Name {
				continue
			}
			ns.Total++
			if d.Online {
				ns.Online++
			}
			if !d.Known {
				ns.Unknown++
			}
		}
		for _, j := range status {
			if j.Network == n.Name {
				ns.Jobs = append(ns.Jobs, j)
			}
		}
		out = append(out, ns)
	}
	return out, nil
}

func (s *server) apiNetworks(w http.ResponseWriter, r *http.Request) {
	nets, err := s.networks(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	if nets == nil {
		nets = []networkSummary{}
	}
	writeJSON(w, nets)
}

func (s *server) apiGraph(w http.ResponseWriter, r *http.Request) {
	network := r.URL.Query().Get("network")
	devs, err := s.Store.Devices(r.Context(), network)
	if err != nil {
		serverError(w, err)
		return
	}
	// All links, even for one network: hops recorded by other networks' traceroutes show the
	// way out and mark which device is this network's gateway.
	links, err := s.Store.Links(r.Context(), "")
	if err != nil {
		serverError(w, err)
		return
	}
	machines, err := s.Store.Machines(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	pings, err := s.Store.Pings(r.Context(), network)
	if err != nil {
		serverError(w, err)
		return
	}
	rt := s.rt()
	// Times measured from elsewhere (before the network got a router, say) don't compare.
	for ref, p := range pings {
		origin := scheduler.NmapOrigin
		if gw := rt.Gateways[ref.Network]; gw != "" {
			origin = gw
		}
		if p.Origin != origin {
			delete(pings, ref)
		}
	}
	var nets []topology.NetInfo
	for _, n := range rt.Config.Networks {
		nets = append(nets, topology.NetInfo{Name: n.Name, CIDR: n.CIDR})
	}
	els := topology.Build(topology.Input{Devices: devs, Links: links, Gateways: rt.Gateways, Machines: machines, Pings: pings,
		Networks: nets, RouterAddrs: routerAddrs(rt.Config), Compound: network == "" && len(nets) > 1})
	if els == nil {
		els = []topology.Element{}
	}
	writeJSON(w, els)
}

func (s *server) apiDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.Store.Devices(r.Context(), r.URL.Query().Get("network"))
	if err != nil {
		serverError(w, err)
		return
	}
	machines, err := s.Store.Machines(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]deviceJSON, 0, len(devs))
	for _, d := range devs {
		out = append(out, toJSON(d, machines[model.MemberRef{Network: d.Network, Key: d.Key}]))
	}
	writeJSON(w, out)
}

func eventFilter(r *http.Request) store.EventFilter {
	q := r.URL.Query()
	f := store.EventFilter{Network: q.Get("network"), Limit: 100}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 1000 {
		f.Limit = v
	}
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && v > 0 {
		f.Before = v
	}
	return f
}

func (s *server) apiEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.Store.Events(r.Context(), eventFilter(r))
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]eventJSON, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventJSON{e.ID, e.Type, e.Network, e.DeviceKey, e.Message, e.Old, e.New, e.At})
	}
	writeJSON(w, out)
}

func (s *server) apiPatchDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label *string `json:"label"`
		Known *bool   `json:"known"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.Label != nil && len(*body.Label) > 200 {
		http.Error(w, "label too long", http.StatusBadRequest)
		return
	}
	err := s.Store.SetDeviceMeta(r.Context(), r.PathValue("key"), body.Label, body.Known)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "device not found", http.StatusNotFound)
	case err != nil:
		serverError(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) apiScan(w http.ResponseWriter, r *http.Request) {
	err := s.rt().Scheduler.ScanNow(r.PathValue("network"))
	switch {
	case errors.Is(err, scheduler.ErrUnknownNetwork):
		http.Error(w, "unknown network", http.StatusNotFound)
	case err != nil:
		serverError(w, err)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

func (s *server) apiJobs(w http.ResponseWriter, r *http.Request) {
	st := s.rt().Scheduler.Status()
	if st == nil {
		st = []scheduler.JobStatus{}
	}
	writeJSON(w, st)
}

func (s *server) apiScanJob(w http.ResponseWriter, r *http.Request) {
	// network/source guard against an id from before a reload pointing at another job.
	id, err := strconv.Atoi(r.PathValue("id"))
	if err == nil {
		err = scheduler.ErrUnknownJob
		q := r.URL.Query()
		for _, j := range s.rt().Scheduler.Status() {
			if j.ID == id && (q.Get("network") == "" || q.Get("network") == j.Network) && (q.Get("source") == "" || q.Get("source") == j.Source) {
				err = s.rt().Scheduler.ScanJob(id)
			}
		}
	} else {
		err = scheduler.ErrUnknownJob
	}
	switch {
	case errors.Is(err, scheduler.ErrUnknownJob):
		http.Error(w, "unknown job", http.StatusNotFound)
	case err != nil:
		serverError(w, err)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

func (s *server) apiNotifyTest(w http.ResponseWriter, r *http.Request) {
	err := s.rt().Notifier.Test(r.Context(), r.PathValue("channel"))
	switch {
	case errors.Is(err, notify.ErrUnknownChannel):
		http.Error(w, "unknown channel", http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- pages ----

type pageData struct {
	Title    string
	Active   string
	Networks []networkSummary
	Devices  []deviceRow
	Device   *deviceView
	Events   []model.Event
	Channels []config.Channel
	Network  string
	Older    int64
	CanEdit  bool
}

func (s *server) render(w http.ResponseWriter, page string, d pageData) {
	d.Active = page
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[page].Execute(w, d); err != nil {
		slog.Error("web: render", "page", page, "err", err)
	}
}

func (s *server) page(name, title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nets, err := s.networks(r.Context())
		if err != nil {
			serverError(w, err)
			return
		}
		s.render(w, name, pageData{Title: title, Networks: nets, Network: r.URL.Query().Get("network"), CanEdit: s.canEdit()})
	}
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	nets, err := s.networks(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	evs, err := s.Store.Events(r.Context(), store.EventFilter{Limit: 15})
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, "dashboard", pageData{Title: "Overview", Networks: nets, Events: evs, Channels: s.rt().Config.Channels})
}

func (s *server) devicesPage(w http.ResponseWriter, r *http.Request) {
	network := r.URL.Query().Get("network")
	nets, err := s.networks(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	devs, err := s.Store.Devices(r.Context(), network)
	if err != nil {
		serverError(w, err)
		return
	}
	machines, err := s.Store.Machines(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	details, err := s.Store.Details(r.Context(), network)
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, "devices", pageData{Title: "Devices", Networks: nets, Devices: groupRows(devs, machines, details), Network: network,
		CanEdit: s.canEdit()})
}

func (s *server) eventsPage(w http.ResponseWriter, r *http.Request) {
	f := eventFilter(r)
	nets, err := s.networks(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	evs, err := s.Store.Events(r.Context(), f)
	if err != nil {
		serverError(w, err)
		return
	}
	var older int64
	if len(evs) == f.Limit {
		older = evs[len(evs)-1].ID
	}
	s.render(w, "events", pageData{Title: "Events", Networks: nets, Events: evs, Network: f.Network, Older: older})
}

// ---- settings ----

type settingsResponse struct {
	settings.Editable
	CanEdit    bool     `json:"can_edit"`
	Path       string   `json:"path"`
	EventTypes []string `json:"event_types"`
}

func (s *server) canEdit() bool { return s.rt().Config.Auth != nil }

func (s *server) apiGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, settingsResponse{Editable: settings.Mask(s.Settings.Raw()), CanEdit: s.canEdit(),
		Path: s.Settings.Path(), EventTypes: model.EventTypes})
}

func writeErrors(w http.ResponseWriter, code int, err error) {
	var lines []string
	for _, l := range strings.Split(err.Error(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"errors": lines})
}

func (s *server) apiPutSettings(w http.ResponseWriter, r *http.Request) {
	if !s.canEdit() {
		http.Error(w, "settings are read-only until auth is configured", http.StatusForbidden)
		return
	}
	var in settings.Editable
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErrors(w, http.StatusBadRequest, fmt.Errorf("invalid request: %w", err))
		return
	}
	raw := s.Settings.Raw()
	if err := s.Settings.Save(settings.Apply(raw, in), settings.NetworkRenames(raw, in)); err != nil {
		writeErrors(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) apiReloadSettings(w http.ResponseWriter, r *http.Request) {
	if err := s.Settings.Reload(); err != nil {
		writeErrors(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// routerAddrs maps each configured router to the IP netmon reaches it at (host names are skipped).
func routerAddrs(c *config.Config) map[string]netip.Addr {
	out := map[string]netip.Addr{}
	for _, r := range c.Routers {
		host := r.Address
		if h, _, err := net.SplitHostPort(r.Address); err == nil {
			host = h
		}
		if ip, err := netip.ParseAddr(host); err == nil {
			out[r.Name] = ip.Unmap()
		}
	}
	return out
}
