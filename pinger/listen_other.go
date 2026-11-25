//go:build !linux

package pinger

import (
	"errors"
	"net"
)

func listenDatagram() (net.PacketConn, error) {
	return nil, errors.New("unprivileged ICMP is only supported on Linux")
}
