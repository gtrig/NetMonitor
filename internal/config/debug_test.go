package config

import (
	"net/netip"
	"testing"
)

func TestDebug(t *testing.T) {
	for _, c := range []struct {
		yaml, addr string
		yes, no    []string
	}{
		{"", "127.0.0.1:8099", []string{"127.0.0.1", "::1", "::ffff:127.0.0.1"}, []string{"192.168.8.2", "fe80::1%eth0"}},
		{"debug: {allow: [192.168.8.1/24]}", ":8099", []string{"192.168.8.200", "::ffff:192.168.8.2"}, []string{"192.168.9.2", "127.0.0.1", "::1"}},
		{"debug: {allow: [127.0.0.0/8]}", "127.0.0.1:8099", []string{"127.0.0.1"}, []string{"::1"}},
		{"debug: {allow: [10.0.0.5/32], listen: 10.0.0.1:9}", "10.0.0.1:9", []string{"10.0.0.5"}, []string{"10.0.0.6"}},
	} {
		cfg, err := Parse([]byte(c.yaml))
		if err != nil {
			t.Fatal(c.yaml, err)
		}
		if got := cfg.Debug.Addr(); got != c.addr {
			t.Errorf("%q: listens on %s, want %s", c.yaml, got, c.addr)
		}
		for _, a := range c.yes {
			if !cfg.Debug.Allows(netip.MustParseAddr(a)) {
				t.Errorf("%q refuses %s", c.yaml, a)
			}
		}
		for _, a := range c.no {
			if cfg.Debug.Allows(netip.MustParseAddr(a)) {
				t.Errorf("%q allows %s", c.yaml, a)
			}
		}
	}
	for _, bad := range []string{"debug: {allow: [lan]}", "debug: {allow: [192.168.8.1]}", "debug: {allow: ['']}", "debug: {listen: '8099'}"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
