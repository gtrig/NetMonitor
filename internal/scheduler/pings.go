package scheduler

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// PingInterval is how often a ping job runs.
const PingInterval = 5 * time.Minute

// NmapOrigin is the origin of round-trip times measured by nmap on the netmon host.
const NmapOrigin = "netmon"

// Pinger measures round-trip times of addresses from one place.
type Pinger interface {
	Name() string
	Origin() string
	// Ping returns the times of the addresses that answered.
	Ping(ctx context.Context, addrs []netip.Addr) (map[netip.Addr]time.Duration, error)
}

func pingable(d model.Device) bool { return d.Online && d.IP.Is4() }

// runPings pings the online IPv4 devices of network. It never touches presence state and creates
// no events. It returns a summary.
func (s *Scheduler) runPings(ctx context.Context, network string, p Pinger) (string, error) {
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return "", err
	}
	asked := map[netip.Addr]bool{}
	var addrs []netip.Addr
	for _, d := range devs {
		if pingable(d) && !asked[d.IP] {
			asked[d.IP] = true
			addrs = append(addrs, d.IP)
		}
	}
	if len(addrs) == 0 {
		return "no devices to ping", nil
	}
	rtts, err := p.Ping(ctx, addrs)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d of %s answered", len(rtts), count(len(addrs), "device")), s.recordRTTs(ctx, network, p.Origin(), rtts, func(a netip.Addr) bool { return asked[a] })
}

// recordRTTs stores one run under the devices' current keys, which may have changed while it ran.
// Online devices in scope without a time are recorded as missed.
func (s *Scheduler) recordRTTs(ctx context.Context, network, origin string, rtts map[netip.Addr]time.Duration, inScope func(netip.Addr) bool) error {
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return err
	}
	var samples []model.PingSample
	for _, d := range devs {
		if !pingable(d) || !inScope(d.IP) {
			continue
		}
		rtt, ok := rtts[d.IP]
		if !ok {
			rtt = -1
		}
		samples = append(samples, model.PingSample{Key: d.Key, RTT: rtt})
	}
	return s.st.RecordPings(ctx, network, origin, time.Now().UTC(), samples)
}

func (s *Scheduler) hasPingJob(network string) bool {
	for _, j := range s.jobs {
		if j.Ping != nil && j.Network.Name == network {
			return true
		}
	}
	return false
}

// recordNmapRTTs stores the times nmap measured, for networks no router pings.
func (s *Scheduler) recordNmapRTTs(ctx context.Context, obs model.Observations) error {
	if obs.Source != "nmap" || s.hasPingJob(obs.Network) {
		return nil
	}
	rtts := map[netip.Addr]time.Duration{}
	for _, h := range obs.Hosts {
		if h.RTT > 0 {
			rtts[h.IP] = h.RTT
		}
	}
	return s.recordRTTs(ctx, obs.Network, NmapOrigin, rtts, func(netip.Addr) bool { return true })
}
