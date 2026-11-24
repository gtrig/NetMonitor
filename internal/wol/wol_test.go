package wol

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
)

func TestPacket(t *testing.T) {
	p, err := Packet("aa:bb:cc:00:11:22")
	if err != nil {
		t.Fatal(err)
	}
	want := append(bytes.Repeat([]byte{0xff}, 6), bytes.Repeat([]byte{0xaa, 0xbb, 0xcc, 0, 0x11, 0x22}, 16)...)
	if !bytes.Equal(p, want) {
		t.Fatalf("packet = %x", p)
	}
	for _, mac := range []string{"", "nope", "00:11:22:33:44:55:66:77"} {
		if _, err := Packet(mac); err == nil {
			t.Errorf("Packet(%q): no error", mac)
		}
	}
}

func TestBroadcast(t *testing.T) {
	for cidr, want := range map[string]string{
		"192.168.88.0/24": "192.168.88.255",
		"192.168.88.7/24": "192.168.88.255",
		"10.1.0.0/22":     "10.1.3.255",
		"10.0.0.4/30":     "10.0.0.7",
	} {
		got, err := Broadcast(netip.MustParsePrefix(cidr))
		if err != nil || got.String() != want {
			t.Errorf("Broadcast(%s) = %v, %v; want %s", cidr, got, err, want)
		}
	}
	for _, cidr := range []string{"10.0.0.0/31", "10.0.0.1/32", "fd00::/64"} {
		if got, err := Broadcast(netip.MustParsePrefix(cidr)); err == nil {
			t.Errorf("Broadcast(%s) = %v, want an error", cidr, got)
		}
	}
	if _, err := Broadcast(netip.Prefix{}); err == nil {
		t.Error("zero prefix: no error")
	}
}

func TestSendPacket(t *testing.T) {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := send("aa:bb:cc:00:11:22", l.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	n, _, err := l.ReadFrom(buf)
	want, _ := Packet("aa:bb:cc:00:11:22")
	if err != nil || !bytes.Equal(buf[:n], want) {
		t.Fatalf("received %x, %v", buf[:n], err)
	}
}
