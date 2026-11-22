package model

import (
	"net/netip"
	"testing"
)

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"AA-BB-CC-00-11-22": "aa:bb:cc:00:11:22",
		"aabb.cc00.1122":    "aa:bb:cc:00:11:22",
		"aa:bb:cc:00:11:22": "aa:bb:cc:00:11:22",
		"zz":                "",
		"":                  "",
	}
	for in, want := range cases {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeviceKey(t *testing.T) {
	ip := netip.MustParseAddr("10.0.0.5")
	if got := DeviceKey(Host{IP: ip, MAC: "AA:BB:CC:00:11:22"}); got != "mac:aa:bb:cc:00:11:22" {
		t.Errorf("got %q", got)
	}
	if got := DeviceKey(Host{IP: ip}); got != "ip:10.0.0.5" {
		t.Errorf("got %q", got)
	}
}
