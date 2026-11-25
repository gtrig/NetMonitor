// Package app owns the running configuration: it builds sources, the scheduler and the
// notification dispatcher from config, saves settings back to the config file and hot-reloads.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/enrich"
	"github.com/gtrig/netmon/internal/notify"
	"github.com/gtrig/netmon/internal/presence"
	"github.com/gtrig/netmon/internal/scheduler"
	"github.com/gtrig/netmon/internal/source"
	"github.com/gtrig/netmon/internal/source/mikrotik"
	"github.com/gtrig/netmon/internal/source/nmap"
	"github.com/gtrig/netmon/internal/source/ping"
	"github.com/gtrig/netmon/internal/source/snmp"
	"github.com/gtrig/netmon/internal/store"
	"go.yaml.in/yaml/v3"
)

// BuildFunc turns an effective config into scheduler jobs and per-network gateway routers.
type BuildFunc func(cfg *config.Config) ([]scheduler.Job, map[string]string)

// Options are the long-lived collaborators shared by every runtime.
type Options struct {
	Store    *store.Store
	OUI      *enrich.OUI
	Resolver *enrich.Resolver
	Build    BuildFunc // nil: DefaultBuild
}

// Runtime is one generation of running components built from one config.
type Runtime struct {
	Config     *config.Config // effective (expanded, validated)
	Scheduler  *scheduler.Scheduler
	Dispatcher *notify.Dispatcher
	Gateways   map[string]string

	cancel context.CancelFunc
	done   chan struct{}
}

// App holds the current runtime and the raw config it came from.
type App struct {
	ctx  context.Context
	path string
	opts Options

	mu  sync.Mutex // serialises Save/Reload
	cur sync.Mutex // guards raw and rt
	raw *config.Config
	rt  *Runtime
}

// New loads path and starts the first runtime. Runtimes stop when ctx is cancelled or Close is called.
func New(ctx context.Context, path string, opts Options) (*App, error) {
	if opts.Build == nil {
		opts.Build = DefaultBuild
	}
	a := &App{ctx: ctx, path: path, opts: opts}
	raw, err := config.LoadRaw(path)
	if err != nil {
		return nil, err
	}
	eff, err := config.Effective(raw)
	if err != nil {
		return nil, err
	}
	rt, err := a.start(eff)
	if err != nil {
		return nil, err
	}
	a.raw, a.rt = raw, rt
	go a.prune(ctx)
	return a, nil
}

// retention is how long ping and job history is kept.
const retention = 30 * 24 * time.Hour

// prune drops old ping and job history now and then daily.
func (a *App) prune(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if err := a.opts.Store.PrunePings(ctx, time.Now().Add(-retention)); err != nil && ctx.Err() == nil {
			slog.Warn("pruning ping history failed", "err", err)
		}
		if err := a.opts.Store.PruneJobRuns(ctx, time.Now().Add(-retention)); err != nil && ctx.Err() == nil {
			slog.Warn("pruning job history failed", "err", err)
		}
		if err := a.opts.Store.PruneGaps(ctx, time.Now().Add(-presence.Window)); err != nil && ctx.Err() == nil {
			slog.Warn("pruning presence gaps failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Path is the config file path.
func (a *App) Path() string { return a.path }

// Current returns the running runtime.
func (a *App) Current() *Runtime {
	a.cur.Lock()
	defer a.cur.Unlock()
	return a.rt
}

// Raw returns a deep copy of the raw config (with ${VAR} references unexpanded).
func (a *App) Raw() *config.Config {
	a.cur.Lock()
	defer a.cur.Unlock()
	return clone(a.raw)
}

func clone(c *config.Config) *config.Config {
	data, err := yaml.Marshal(c)
	if err != nil {
		panic(fmt.Sprintf("clone config: %v", err)) // only plain data types; cannot fail
	}
	var out config.Config
	if err := yaml.Unmarshal(data, &out); err != nil {
		panic(fmt.Sprintf("clone config: %v", err))
	}
	return &out
}

// Save validates raw, writes it to the config file and switches to a runtime built from it.
// Nothing is written or changed when raw is invalid.
// renames (old network name → new) move stored data to the new names before the new runtime starts.
func (a *App) Save(raw *config.Config, renames map[string]string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	eff, err := config.Effective(raw)
	if err != nil {
		return err
	}
	if err := config.Save(a.path, raw); err != nil {
		return err
	}
	return a.swap(clone(raw), eff, renames)
}

// Reload re-reads the config file. The current runtime keeps running if the file is invalid.
func (a *App) Reload() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, err := config.LoadRaw(a.path)
	if err != nil {
		return err
	}
	eff, err := config.Effective(raw)
	if err != nil {
		return err
	}
	return a.swap(raw, eff, nil)
}

func (a *App) swap(raw, eff *config.Config, renames map[string]string) error {
	old := a.Current()
	if eff.Listen != old.Config.Listen || eff.DataDir != old.Config.DataDir {
		slog.Warn("listen/data_dir changes take effect after a restart")
		eff.Listen, eff.DataDir = old.Config.Listen, old.Config.DataDir
	}
	if eff.Debug.Addr() != old.Config.Debug.Addr() {
		slog.Warn("the debug server keeps its address until a restart", "new", eff.Debug.Addr())
	}
	if _, err := notify.FromConfig(eff.Channels); err != nil {
		return err // fail before stopping the running runtime
	}
	old.stop()
	for _, r := range orderRenames(renames) {
		from, to := r[0], r[1]
		if err := a.opts.Store.RenameNetwork(context.Background(), from, to); err != nil {
			slog.Error("moving stored data of renamed network", "from", from, "to", to, "err", err)
		}
	}
	rt, err := a.start(eff)
	if err != nil {
		return err
	}
	a.cur.Lock()
	a.raw, a.rt = raw, rt
	a.cur.Unlock()
	slog.Info("configuration applied", "networks", len(eff.Networks), "channels", len(eff.Channels))
	return nil
}

func (a *App) start(eff *config.Config) (*Runtime, error) {
	notifiers, err := notify.FromConfig(eff.Channels)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	st := a.opts.Store
	d := notify.NewDispatcher(eff.Rules, notifiers, func(key string) bool { return st.IsKnown(context.Background(), key) })
	d.Start(ctx)
	jobs, gateways := a.opts.Build(eff)
	sched := scheduler.New(st, d, a.opts.OUI, a.opts.Resolver, jobs)
	sched.DefaultDetails = nmap.NewServices(nil, nil)
	sched.SetWorkers(eff.Workers)
	rt := &Runtime{Config: eff, Scheduler: sched, Dispatcher: d, Gateways: gateways, cancel: cancel, done: make(chan struct{})}
	go func() { sched.Run(ctx); close(rt.done) }()
	return rt, nil
}

// stop cancels the runtime and waits for in-flight collections to finish.
func (rt *Runtime) stop() {
	rt.cancel()
	<-rt.done
}

// Close stops the current runtime.
func (a *App) Close() {
	if rt := a.Current(); rt != nil {
		rt.stop()
	}
}

// DefaultBuild creates real MikroTik, SNMP, nmap and ping sources from cfg.
func DefaultBuild(cfg *config.Config) ([]scheduler.Job, map[string]string) {
	// Map router management addresses to configured names so neighbors and SNMP views of
	// the same router land on one map node, even when identities collide.
	byAddr := map[string]string{}
	for _, r := range cfg.Routers {
		host, _, err := net.SplitHostPort(r.Address)
		if err != nil {
			host = r.Address
		}
		byAddr[host] = r.Name
	}
	resolve := func(address, identity string) string {
		if name, ok := byAddr[address]; ok {
			return name
		}
		return identity
	}
	routers := map[string]*mikrotik.Source{}
	byName := map[string]config.Router{}
	for _, r := range cfg.Routers {
		routers[r.Name] = mikrotik.New(r, resolve)
		byName[r.Name] = r
	}
	targets := map[string]*snmp.Source{}
	for _, t := range cfg.SNMPTargets {
		targets[t.Name] = snmp.New(t, resolve)
	}
	var jobs []scheduler.Job
	gateways := map[string]string{}
	for _, n := range cfg.Networks {
		for _, ref := range n.Sources {
			var src source.Source
			switch ref.Type {
			case "router":
				src = routers[ref.Ref]
				if gateways[n.Name] == "" {
					gateways[n.Name] = ref.Ref
				}
			case "snmp":
				src = targets[ref.Ref]
			case "nmap":
				src = nmap.New(ref.Args, ref.Traceroute, nil)
			case "ping":
				jobs = append(jobs, scheduler.Job{Network: n, Sweep: ping.Sweeper{}, Interval: ref.Interval.Duration})
				continue
			case "services":
				jobs = append(jobs, scheduler.Job{Network: n, Details: nmap.NewServices(ref.Args, nil), Interval: ref.Interval.Duration})
				continue
			}
			jobs = append(jobs, scheduler.Job{Network: n, Source: src, Interval: ref.Interval.Duration, OfflineAfter: n.OfflineAfterFor(ref)})
		}
	}
	for _, n := range cfg.Networks {
		if r, ok := byName[gateways[n.Name]]; ok {
			jobs = append(jobs, scheduler.Job{Network: n, Ping: mikrotik.NewPinger(r), Interval: scheduler.PingInterval})
		}
	}
	return jobs, gateways
}

// orderRenames turns old→new network renames into steps that can run one after another: a
// name is renamed away before anything is renamed onto it, and swaps go through a temporary name.
func orderRenames(renames map[string]string) [][2]string {
	pending := maps.Clone(renames)
	var steps [][2]string
	for len(pending) > 0 {
		progressed := false
		for _, from := range slices.Sorted(maps.Keys(pending)) {
			to := pending[from]
			if _, busy := pending[to]; busy {
				continue // to still holds data that must move first
			}
			steps = append(steps, [2]string{from, to})
			delete(pending, from)
			progressed = true
		}
		if !progressed { // only cycles left: park one name under a temporary one
			from := slices.Sorted(maps.Keys(pending))[0]
			tmp := "\x00rename:" + from
			steps = append(steps, [2]string{from, tmp})
			pending[tmp] = pending[from]
			delete(pending, from)
		}
	}
	return steps
}
