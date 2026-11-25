package pinger

import (
	"net"
	"os"
	"syscall"
)

func listenDatagram() (net.PacketConn, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, syscall.IPPROTO_ICMP)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	f := os.NewFile(uintptr(fd), "icmp")
	defer f.Close() // FilePacketConn duplicates it
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, readBuffer)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{}); err != nil {
		return nil, os.NewSyscallError("bind", err)
	}
	return net.FilePacketConn(f)
}
