// Package mikrotik collects hosts and topology from MikroTik routers via the RouterOS API.
package mikrotik

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/go-routeros/routeros/v3"
	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

// Runner runs a RouterOS print command and returns its rows.
type Runner interface {
	Print(ctx context.Context, path string) ([]map[string]string, error)
}

// Source is a RouterOS API source. Its router node ID is the configured router name, so two
// routers with the same (e.g. factory default "MikroTik") identity stay distinct.
type Source struct {
	name    string
	run     Runner
	resolve Resolver
}

func identityResolver(_, identity string) string { return identity }

// New returns a source that connects to the router described by r. resolve maps neighbor
// routers to node IDs (nil: use their identity).
func New(r config.Router, resolve Resolver) *Source {
	return (&Source{name: r.Name, run: &apiRunner{cfg: r}}).WithResolver(resolve)
}

// NewWithRunner returns a source using run (for tests).
func NewWithRunner(name string, run Runner) *Source {
	return &Source{name: name, run: run, resolve: identityResolver}
}

// WithResolver sets the neighbor resolver (nil: identity).
func (s *Source) WithResolver(resolve Resolver) *Source {
	if resolve == nil {
		resolve = identityResolver
	}
	s.resolve = resolve
	return s
}

func (s *Source) Name() string { return "router:" + s.name }

// Collect reads identity, ARP, DHCP leases, bridge hosts and neighbors.
// Only a failing ARP read fails the collection; the rest are best-effort.
func (s *Source) Collect(ctx context.Context, n config.Network) (model.Observations, error) {
	obs := model.Observations{Network: n.Name, Source: s.Name(), At: time.Now().UTC()}
	arp, err := s.run.Print(ctx, "/ip/arp/print")
	if err != nil {
		return obs, fmt.Errorf("%s: arp: %w", s.Name(), err)
	}
	optional := func(path string) []map[string]string {
		rows, err := s.run.Print(ctx, path)
		if err != nil {
			slog.Warn("mikrotik: optional read failed", "router", s.name, "path", path, "err", err)
		}
		return rows
	}
	names := parseLeases(optional("/ip/dhcp-server/lease/print"))
	ports := parseBridgeHosts(optional("/interface/bridge/host/print"))
	obs.Links = parseNeighbors(s.name, optional("/ip/neighbor/print"), s.resolve)

	router := model.NodeRef{Kind: "router", ID: s.name}
	for _, h := range parseARP(arp) {
		h.Source = s.Name()
		h.Hostname = names[h.MAC]
		if p, ok := ports[h.MAC]; ok {
			h.Port = p
		}
		obs.Hosts = append(obs.Hosts, h)
		obs.Links = append(obs.Links, model.Link{A: router, B: model.NodeRef{Kind: "device", ID: model.DeviceKey(h)}, Label: h.Port})
	}
	for i := range obs.Links {
		obs.Links[i].Source = s.Name()
	}
	return obs, nil
}

// apiRunner talks to a real router, dialing lazily and reconnecting after errors.
type apiRunner struct {
	cfg    config.Router
	mu     sync.Mutex
	client *routeros.Client
}

func (a *apiRunner) connect(ctx context.Context) (*routeros.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", a.cfg.Address)
	if err != nil {
		return nil, err
	}
	if a.cfg.TLS {
		host, _, _ := net.SplitHostPort(a.cfg.Address)
		tc := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: a.cfg.InsecureSkipVerify}) //nolint:gosec // opt-in for self-signed RouterOS certs
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	// The RouterOS client ignores ctx once connected; closing the conn unblocks it.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c, err := routeros.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.Login(a.cfg.Username, a.cfg.Password); err != nil {
		c.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	a.client = c
	return c, nil
}

func (a *apiRunner) Print(ctx context.Context, path string) ([]map[string]string, error) {
	return a.Run(ctx, path)
}

// Run runs a command with API arguments ("=name=value") and returns its rows.
func (a *apiRunner) Run(ctx context.Context, path string, args ...string) ([]map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.connect(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	reply, err := c.Run(append([]string{path}, args...)...)
	if !stop() || err != nil {
		c.Close()
		a.client = nil
		if err == nil || ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", path, context.Cause(ctx))
		}
		return nil, err
	}
	rows := make([]map[string]string, 0, len(reply.Re))
	for _, re := range reply.Re {
		rows = append(rows, re.Map)
	}
	return rows, nil
}
