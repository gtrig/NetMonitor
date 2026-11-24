// Package wol sends Wake-on-LAN magic packets.
package wol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
)

// port is "discard", the usual Wake-on-LAN port.
const port = 9

// Send broadcasts a magic packet for mac on network. There is no reply: success only means
// that the packet was sent.
func Send(mac string, network netip.Prefix) error {
	addr, err := Broadcast(network)
	if err != nil {
		return err
	}
	return send(mac, netip.AddrPortFrom(addr, port))
}

func send(mac string, to netip.AddrPort) error {
	p, err := Packet(mac)
	if err != nil {
		return err
	}
	// Go enables SO_BROADCAST on UDP sockets.
	c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(to))
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(p)
	return err
}

// Packet is the magic packet for mac: six 0xff bytes, then the address 16 times.
func Packet(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return nil, err
	}
	if len(hw) != 6 {
		return nil, fmt.Errorf("%q is not an Ethernet address", mac)
	}
	return append(bytes.Repeat([]byte{0xff}, 6), bytes.Repeat(hw, 16)...), nil
}

// Broadcast is the directed broadcast address of an IPv4 network.
func Broadcast(n netip.Prefix) (netip.Addr, error) {
	if !n.IsValid() || !n.Addr().Is4() || n.Bits() > 30 {
		return netip.Addr{}, fmt.Errorf("network %s has no broadcast address", n)
	}
	a := n.Addr().As4()
	binary.BigEndian.PutUint32(a[:], binary.BigEndian.Uint32(a[:])|^uint32(0)>>n.Bits())
	return netip.AddrFrom4(a), nil
}
