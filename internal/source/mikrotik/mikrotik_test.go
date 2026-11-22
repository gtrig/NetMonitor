package mikrotik

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/gtrig/netmon/internal/config"
)

type fakeRunner struct {
	rows map[string][]map[string]string
	errs map[string]error
}

func (f fakeRunner) Print(ctx context.Context, path string) ([]map[string]string, error) {
	if err := f.errs[path]; err != nil {
		return nil, err
	}
	return f.rows[path], nil
}

var lan = config.Network{Name: "lan", CIDR: netip.MustParsePrefix("192.168.88.0/24")}

func baseRows() map[string][]map[string]string {
	return map[string][]map[string]string{
		"/system/identity/print": {{"name": "core"}},
		"/ip/arp/print": {
			{"address": "192.168.88.10", "mac-address": "AA:00:00:00:00:10", "interface": "bridge", "status": "reachable"},
			{"address": "192.168.88.11", "mac-address": "AA:00:00:00:00:11", "interface": "bridge", "status": "stale"},
			{"address": "192.168.88.12", "interface": "bridge", "status": "incomplete"},
			{"address": "192.168.88.13", "mac-address": "AA:00:00:00:00:13", "status": "failed"},
		},
		"/ip/dhcp-server/lease/print": {
			{"address": "192.168.88.10", "mac-address": "AA:00:00:00:00:10", "host-name": "nas", "status": "bound"},
			{"address": "192.168.88.99", "mac-address": "AA:00:00:00:00:99", "host-name": "gone", "status": "bound"},
		},
		"/interface/bridge/host/print": {
			{"mac-address": "AA:00:00:00:00:10", "on-interface": "ether2", "bridge": "bridge"},
			{"mac-address": "48:A9:8A:00:00:01", "on-interface": "bridge", "bridge": "bridge", "local": "true"},
		},
		"/ip/neighbor/print": {
			{"identity": "ap-attic", "interface": "ether5", "address": "192.168.88.2", "mac-address": "48:A9:8A:00:00:02"},
		},
	}
}

func TestParseARPSkipsIncomplete(t *testing.T) {
	hosts := parseARP(baseRows()["/ip/arp/print"])
	if len(hosts) != 2 {
		t.Fatalf("hosts = %+v", hosts)
	}
	if hosts[0].MAC != "aa:00:00:00:00:10" || hosts[0].IP.String() != "192.168.88.10" {
		t.Fatalf("first = %+v", hosts[0])
	}
}

func TestCollectUsesLeaseHostname(t *testing.T) {
	s := NewWithRunner("core", fakeRunner{rows: baseRows()})
	o, err := s.Collect(context.Background(), lan)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Hosts) != 2 {
		t.Fatalf("lease must not add presence: %+v", o.Hosts)
	}
	if o.Hosts[0].Hostname != "nas" || o.Hosts[0].Port != "ether2" {
		t.Fatalf("host = %+v", o.Hosts[0])
	}
	if s.Name() != "router:core" || o.Source != "router:core" {
		t.Fatalf("name = %q / %q", s.Name(), o.Source)
	}
}

func TestCollectNeighborLinks(t *testing.T) {
	o, err := NewWithRunner("core", fakeRunner{rows: baseRows()}).Collect(context.Background(), lan)
	if err != nil {
		t.Fatal(err)
	}
	var routerLink, portLink bool
	for _, l := range o.Links {
		if l.A.Kind == "router" && l.A.ID == "core" && l.B.Kind == "router" && l.B.ID == "ap-attic" && l.Label == "ether5" {
			routerLink = true
		}
		if l.A.ID == "core" && l.B.Kind == "device" && l.B.ID == "mac:aa:00:00:00:00:10" && l.Label == "ether2" {
			portLink = true
		}
	}
	if !routerLink || !portLink {
		t.Fatalf("links = %+v", o.Links)
	}
}

func TestCollectARPErrorFails(t *testing.T) {
	_, err := NewWithRunner("core", fakeRunner{rows: baseRows(), errs: map[string]error{"/ip/arp/print": errors.New("boom")}}).
		Collect(context.Background(), lan)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectLeaseErrorStillReturnsHosts(t *testing.T) {
	o, err := NewWithRunner("core", fakeRunner{rows: baseRows(), errs: map[string]error{
		"/ip/dhcp-server/lease/print": errors.New("no dhcp"), "/ip/neighbor/print": errors.New("no perms")}}).
		Collect(context.Background(), lan)
	if err != nil || len(o.Hosts) != 2 {
		t.Fatalf("hosts=%+v err=%v", o.Hosts, err)
	}
}
