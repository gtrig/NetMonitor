// Command netmon monitors networks, serves a topology map and sends change notifications.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/enrich"
	"github.com/gtrig/netmon/internal/notify"
	"github.com/gtrig/netmon/internal/scheduler"
	"github.com/gtrig/netmon/internal/source"
	"github.com/gtrig/netmon/internal/source/mikrotik"
	"github.com/gtrig/netmon/internal/source/nmap"
	"github.com/gtrig/netmon/internal/source/snmp"
	"github.com/gtrig/netmon/internal/store"
	"github.com/gtrig/netmon/internal/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("netmon exiting", "err", err)
		os.Exit(1)
	}
}

func run() error {
	defPath := os.Getenv("NETMON_CONFIG")
	if defPath == "" {
		defPath = "./config/config.yaml"
	}
	cfgPath := flag.String("config", defPath, "path to config.yaml (env NETMON_CONFIG)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if host, _, err := net.SplitHostPort(cfg.Listen); err == nil && cfg.Auth == nil {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			slog.Warn("web UI is reachable from the network WITHOUT authentication; set auth: in the config", "listen", cfg.Listen)
		}
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "netmon.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	oui, err := enrich.LoadOUI(cfg.OUIPath)
	if err != nil {
		slog.Warn("vendor lookup disabled", "oui_path", cfg.OUIPath, "err", err)
	}
	resolver := enrich.NewResolver(time.Hour)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	notifiers, err := notify.FromConfig(cfg.Channels)
	if err != nil {
		return err
	}
	dispatcher := notify.NewDispatcher(cfg.Rules, notifiers, func(key string) bool { return st.IsKnown(context.Background(), key) })
	dispatcher.Start(ctx)

	jobs, gateways := buildJobs(cfg)
	sched := scheduler.New(st, dispatcher, oui, resolver, jobs)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.New(web.Deps{Store: st, Scheduler: sched, Notifier: dispatcher, Config: cfg, Gateways: gateways}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpErr := make(chan error, 1)
	go func() {
		slog.Info("netmon listening", "addr", cfg.Listen, "networks", len(cfg.Networks), "jobs", len(jobs))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
		}
		close(httpErr)
	}()
	schedDone := make(chan struct{})
	go func() { sched.Run(ctx); close(schedDone) }()

	select {
	case <-ctx.Done():
	case err := <-httpErr:
		stop()
		<-schedDone
		return err
	}
	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	<-schedDone
	return nil
}

// buildJobs creates one job per configured source and the per-network gateway router.
func buildJobs(cfg *config.Config) ([]scheduler.Job, map[string]string) {
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
	for _, r := range cfg.Routers {
		routers[r.Name] = mikrotik.New(r, resolve)
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
				src = nmap.New(ref.Args, nil)
			}
			jobs = append(jobs, scheduler.Job{Network: n, Source: src, Interval: ref.Interval.Duration, OfflineAfter: n.OfflineAfterFor(ref)})
		}
	}
	return jobs, gateways
}
