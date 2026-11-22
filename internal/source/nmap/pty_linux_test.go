package nmap

import (
	"syscall"
	"testing"
	"unsafe"
)

func TestOpenPTYGivesATerminal(t *testing.T) {
	m, s, err := openPTY()
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	defer m.Close()
	defer s.Close()
	var tios syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, s.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&tios))); e != 0 {
		t.Fatalf("slave is not a terminal: %v", e)
	}
}
