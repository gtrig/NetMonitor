// Package ping finds active hosts with concurrent ICMP sweeps from the netmon host.
package ping

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"

	"github.com/gtrig/netmon/internal/source"
	"github.com/gtrig/netmon/pinger"
)

// Name is the source's name.
const Name = "ping"

// Sweeper pings addresses; the zero value is ready to use.
type Sweeper struct{}

func (Sweeper) Name() string { return Name }

// Sweep pings every address count times and returns the average times of those that answered.
func (Sweeper) Sweep(ctx context.Context, addrs []netip.Addr, count int, gap time.Duration) (map[netip.Addr]time.Duration, error) {
	found, err := pinger.Sweep(ctx, addrs, pinger.Options{Count: count, Gap: gap})
	if err != nil {
		return nil, err
	}
	out := make(map[netip.Addr]time.Duration, len(found))
	for a, st := range found {
		out[a] = st.Avg
	}
	return out, nil
}

// Reachable reports whether a host that does not answer pings accepts or refuses TCP connections.
func (Sweeper) Reachable(ctx context.Context, addr netip.Addr) bool {
	ports := []uint16{22, 80, 443, 445}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	alive := make(chan bool, len(ports))
	for _, p := range ports {
		go func() { alive <- dial(ctx, netip.AddrPortFrom(addr, p)) }()
	}
	for range ports {
		if <-alive {
			return true
		}
	}
	return false
}

func dial(ctx context.Context, to netip.AddrPort) bool {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(to.Addr().String(), strconv.Itoa(int(to.Port()))))
	if err != nil {
		return errors.Is(err, syscall.ECONNREFUSED)
	}
	c.Close()
	return true
}

// Neighbors returns the hosts on attached networks whose MAC addresses the system knows.
func (Sweeper) Neighbors() map[netip.Addr]source.Neighbor { return neighbors() }
