// Package mikrotik collects hosts and topology from MikroTik routers via the RouterOS API.
package mikrotik

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
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

// Source is a RouterOS API source.
type Source struct {
	name string
	run  Runner
}

// New returns a source that connects to the router described by r.
func New(r config.Router) *Source {
	return &Source{name: r.Name, run: &apiRunner{cfg: r}}
}

// NewWithRunner returns a source using run (for tests).
func NewWithRunner(name string, run Runner) *Source { return &Source{name: name, run: run} }

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
	identity := s.name
	if rows := optional("/system/identity/print"); len(rows) > 0 && rows[0]["name"] != "" {
		identity = rows[0]["name"]
	}
	names := parseLeases(optional("/ip/dhcp-server/lease/print"))
	ports := parseBridgeHosts(optional("/interface/bridge/host/print"))
	obs.Links = parseNeighbors(identity, optional("/ip/neighbor/print"))

	router := model.NodeRef{Kind: "router", ID: identity}
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
	var c *routeros.Client
	var err error
	if a.cfg.TLS {
		c, err = routeros.DialTLSContext(ctx, a.cfg.Address, a.cfg.Username, a.cfg.Password,
			&tls.Config{InsecureSkipVerify: a.cfg.InsecureSkipVerify}) //nolint:gosec // opt-in for self-signed RouterOS certs
	} else {
		c, err = routeros.DialContext(ctx, a.cfg.Address, a.cfg.Username, a.cfg.Password)
	}
	if err != nil {
		return nil, err
	}
	a.client = c
	return c, nil
}

func (a *apiRunner) Print(ctx context.Context, path string) ([]map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.connect(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reply, err := c.RunContext(ctx, path)
	if err != nil {
		c.Close()
		a.client = nil
		return nil, err
	}
	rows := make([]map[string]string, 0, len(reply.Re))
	for _, re := range reply.Re {
		rows = append(rows, re.Map)
	}
	return rows, nil
}
