package ping

import (
	"encoding/binary"
	"log/slog"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/source"
)

// From linux/neighbour.h.
const (
	ndmsgLen     = 12
	ndaDst       = 1
	ndaLLAddr    = 2
	ndaCacheInfo = 3
	nudKnown     = 0x02 | 0x04 | 0x08 | 0x10 | 0x80 // reachable, stale, delay, probe, permanent
	nudPermanent = 0x80
	userHZ       = 100 // ticks per second of cache times, on every Linux port Go supports
)

func neighbors() map[netip.Addr]source.Neighbor {
	// The file /proc/net/arp has no times, and lists hosts as complete long after they left.
	b, err := syscall.NetlinkRIB(syscall.RTM_GETNEIGH, syscall.AF_INET)
	if err != nil {
		slog.Debug("reading neighbors failed", "err", err)
		return nil
	}
	return parseNeighbors(b, time.Now().UTC())
}

func parseNeighbors(b []byte, now time.Time) map[netip.Addr]source.Neighbor {
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return nil
	}
	out := map[netip.Addr]source.Neighbor{}
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWNEIGH || len(m.Data) < ndmsgLen || m.Data[0] != syscall.AF_INET {
			continue
		}
		state := binary.NativeEndian.Uint16(m.Data[8:])
		if state&nudKnown == 0 {
			continue
		}
		var ip netip.Addr
		var n source.Neighbor
		for a := m.Data[ndmsgLen:]; len(a) >= 4; {
			size, typ := int(binary.NativeEndian.Uint16(a)), binary.NativeEndian.Uint16(a[2:])
			if size < 4 || size > len(a) {
				break
			}
			v := a[4:size]
			switch {
			case typ == ndaDst:
				ip, _ = netip.AddrFromSlice(v)
			case typ == ndaLLAddr && len(v) == 6:
				n.MAC = model.NormalizeMAC(net.HardwareAddr(v).String())
			case typ == ndaCacheInfo && len(v) >= 4 && state&nudPermanent == 0:
				n.Confirmed = now.Add(-time.Duration(binary.NativeEndian.Uint32(v)) * time.Second / userHZ)
			}
			a = a[min(len(a), (size+3)&^3):]
		}
		if ip.Is4() && n.MAC != "" && n.MAC != "00:00:00:00:00:00" {
			out[ip] = n
		}
	}
	return out
}
