package enrich

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Resolver does cached reverse DNS lookups.
type Resolver struct {
	ttl   time.Duration
	mu    sync.Mutex
	cache map[netip.Addr]cached
	look  func(ctx context.Context, addr string) ([]string, error)
}

type cached struct {
	name string
	at   time.Time
}

// NewResolver returns a resolver caching results (including misses) for ttl.
func NewResolver(ttl time.Duration) *Resolver {
	return &Resolver{ttl: ttl, cache: map[netip.Addr]cached{}, look: net.DefaultResolver.LookupAddr}
}

// Name returns the reverse DNS name of ip without trailing dot, or "".
func (r *Resolver) Name(ctx context.Context, ip netip.Addr) string {
	r.mu.Lock()
	c, ok := r.cache[ip]
	r.mu.Unlock()
	if ok && time.Since(c.at) < r.ttl {
		return c.name
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	name := ""
	if names, err := r.look(ctx, ip.String()); err == nil && len(names) > 0 {
		name = strings.TrimSuffix(names[0], ".")
	}
	r.mu.Lock()
	r.cache[ip] = cached{name: name, at: time.Now()}
	r.mu.Unlock()
	return name
}
