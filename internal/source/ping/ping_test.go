package ping

import (
	"context"
	"net"
	"testing"
)

func TestDial(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	to := l.Addr().(*net.TCPAddr).AddrPort()
	if !dial(context.Background(), to) {
		t.Error("accepting port: not alive")
	}
	l.Close()
	if !dial(context.Background(), to) {
		t.Error("refusing port: not alive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if dial(ctx, to) {
		t.Error("cancelled: alive")
	}
}
