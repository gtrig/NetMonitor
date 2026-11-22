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

	"github.com/gtrig/netmon/internal/app"
	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/enrich"
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

	a, err := app.New(ctx, *cfgPath, app.Options{Store: st, OUI: oui, Resolver: resolver})
	if err != nil {
		return err
	}
	defer a.Close()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := a.Reload(); err != nil {
				slog.Error("reload failed; keeping current configuration", "err", err)
			}
		}
	}()

	runtime := func() web.Runtime {
		rt := a.Current()
		return web.Runtime{Scheduler: rt.Scheduler, Notifier: rt.Dispatcher, Config: rt.Config, Gateways: rt.Gateways}
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.New(web.Deps{Store: st, Runtime: runtime, Settings: a}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpErr := make(chan error, 1)
	go func() {
		slog.Info("netmon listening", "addr", cfg.Listen, "config", *cfgPath, "networks", len(cfg.Networks))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
		}
		close(httpErr)
	}()

	select {
	case <-ctx.Done():
	case err := <-httpErr:
		return err
	}
	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	return nil // deferred a.Close waits for in-flight collections
}
