package mikrotik

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
)

// Finding 1: a router that accepts TCP but never answers must not hang Collect past ctx.
func TestCollectHonoursContextWhenRouterStalls(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // hold open, never reply
		}
	}()
	s := New(config.Router{Name: "core", Address: ln.Addr().String(), Username: "u", Password: "p"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = s.Collect(ctx, lan)
	if err == nil {
		t.Fatal("expected error")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Collect took %v", el)
	}
}

// Finding 4: two routers with the factory identity "MikroTik" stay distinct.
func TestRouterNodesKeyedByConfigName(t *testing.T) {
	rows := baseRows()
	rows["/system/identity/print"] = []map[string]string{{"name": "MikroTik"}}
	rows["/ip/neighbor/print"] = []map[string]string{{"identity": "MikroTik", "address": "192.168.88.2", "interface": "ether5"}}
	resolve := func(addr, identity string) string {
		if addr == "192.168.88.2" {
			return "ap"
		}
		return identity
	}
	o, err := NewWithRunner("core", fakeRunner{rows: rows}).WithResolver(resolve).Collect(context.Background(), lan)
	if err != nil {
		t.Fatal(err)
	}
	var inter, host bool
	for _, l := range o.Links {
		if l.A.ID == "core" && l.B.Kind == "router" && l.B.ID == "ap" {
			inter = true
		}
		if l.A.ID == "core" && l.B.Kind == "device" {
			host = true
		}
	}
	if !inter || !host {
		t.Fatalf("links = %+v", o.Links)
	}
}

// Finding 5: static and DHCP-created ARP entries don't prove presence.
func TestParseARPSkipsStaticAndDHCPEntries(t *testing.T) {
	hosts := parseARP([]map[string]string{
		{"address": "192.168.88.20", "mac-address": "AA:00:00:00:00:20", "status": "permanent", "dynamic": "false"},
		{"address": "192.168.88.21", "mac-address": "AA:00:00:00:00:21", "status": "reachable", "dhcp": "true"},
		{"address": "192.168.88.22", "mac-address": "AA:00:00:00:00:22", "status": "stale", "dynamic": "true"},
	})
	if len(hosts) != 1 || hosts[0].IP.String() != "192.168.88.22" {
		t.Fatalf("hosts = %+v", hosts)
	}
}
