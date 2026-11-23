package mikrotik

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/go-routeros/routeros/v3"
	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/source"
)

// ErrPingDenied is returned when the router user may not run /ping.
var ErrPingDenied = errors.New(`router user needs the "test" policy to ping`)

// maxPingErrors is how many failures in a row (not missed replies) end a run.
const maxPingErrors = 3

type commandRunner interface {
	Run(ctx context.Context, path string, args ...string) ([]map[string]string, error)
}

// Pinger measures round-trip times from a router, on its own API connection.
type Pinger struct {
	name string
	run  commandRunner
}

// NewPinger returns a pinger for the router described by r.
func NewPinger(r config.Router) *Pinger { return &Pinger{name: r.Name, run: &apiRunner{cfg: r}} }

func (p *Pinger) Name() string { return "ping:" + p.name }

// Origin is the router the times are measured from.
func (p *Pinger) Origin() string { return p.name }

// Ping pings each address three times, one address at a time, and returns the median time of
// those that answered.
func (p *Pinger) Ping(ctx context.Context, addrs []netip.Addr) (map[netip.Addr]time.Duration, error) {
	out := map[netip.Addr]time.Duration{}
	var lastErr error
	failed, inRow := 0, 0
	for i, a := range addrs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		source.ReportProgress(ctx, source.Progress{Task: "Ping", Percent: 100 * float64(i) / float64(len(addrs))})
		rows, err := p.run.Run(ctx, "/ping", "=address="+a.String(), "=count=3", "=interval=200ms")
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if strings.Contains(err.Error(), "not enough permissions") {
				return out, fmt.Errorf("%s: %w", p.Name(), ErrPingDenied)
			}
			lastErr = err
			failed++
			var refused *routeros.DeviceError // the router answered: it is not dead
			if errors.As(err, &refused) {
				inRow = 0
				continue
			}
			if inRow++; inRow >= maxPingErrors {
				return out, fmt.Errorf("%s: %w", p.Name(), err)
			}
			continue
		}
		inRow = 0
		var times []time.Duration
		for _, r := range rows {
			if d, ok := parsePingTime(r["time"]); ok {
				times = append(times, d)
			}
		}
		if n := len(times); n > 0 {
			slices.Sort(times)
			out[a] = (times[(n-1)/2] + times[n/2]) / 2
		}
	}
	if failed > 0 && failed == len(addrs) {
		return out, fmt.Errorf("%s: %w", p.Name(), lastErr)
	}
	return out, nil
}

// parsePingTime reads RouterOS times: "1ms234us", "12ms", or the older "00:00:00.010".
func parsePingTime(s string) (time.Duration, bool) {
	if parts := strings.Split(s, ":"); len(parts) == 3 {
		s = parts[0] + "h" + parts[1] + "m" + parts[2] + "s"
	}
	d, err := time.ParseDuration(s)
	return d, err == nil && d >= 0
}
