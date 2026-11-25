package ping

import (
	"encoding/binary"
	"net/netip"
	"syscall"
	"testing"
	"time"
)

func neighMsg(ip [4]byte, state uint16, mac []byte, confirmed uint32) []byte {
	ne := binary.NativeEndian
	attr := func(typ uint16, v []byte) []byte {
		b := ne.AppendUint16(ne.AppendUint16(nil, uint16(4+len(v))), typ)
		b = append(b, v...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
		return b
	}
	d := make([]byte, ndmsgLen)
	d[0] = syscall.AF_INET
	ne.PutUint16(d[8:], state)
	d = append(d, attr(ndaDst, ip[:])...)
	d = append(d, attr(ndaLLAddr, mac)...)
	d = append(d, attr(ndaCacheInfo, append(ne.AppendUint32(nil, confirmed), make([]byte, 12)...))...)
	h := ne.AppendUint32(nil, uint32(syscall.NLMSG_HDRLEN+len(d)))
	h = ne.AppendUint16(h, syscall.RTM_NEWNEIGH)
	return append(append(h, make([]byte, 10)...), d...)
}

func TestParseNeighbors(t *testing.T) {
	now := time.Now()
	mac := []byte{0xaa, 0, 0, 0, 0, 1}
	var b []byte
	b = append(b, neighMsg([4]byte{10, 0, 0, 1}, 0x02, mac, 250)...)
	b = append(b, neighMsg([4]byte{10, 0, 0, 2}, 0x20, mac, 0)...) // failed
	b = append(b, neighMsg([4]byte{10, 0, 0, 3}, 0x80, mac, 0)...) // permanent proves nothing
	b = append(b, neighMsg([4]byte{10, 0, 0, 4}, 0x02, make([]byte, 6), 0)...)
	got := parseNeighbors(b, now)
	if n := got[netip.MustParseAddr("10.0.0.1")]; n.MAC != "aa:00:00:00:00:01" || now.Sub(n.Confirmed) != 2500*time.Millisecond {
		t.Errorf("%+v", n)
	}
	if n := got[netip.MustParseAddr("10.0.0.3")]; n.MAC == "" || !n.Confirmed.IsZero() {
		t.Errorf("permanent: %+v", n)
	}
	if len(got) != 2 {
		t.Errorf("%v", got)
	}
}
