package nmap

import (
	"context"
	"net/netip"
	"os"
	"slices"
	"testing"
)

func TestParseServicesXML(t *testing.T) {
	data, _ := os.ReadFile("testdata/services.xml")
	hosts, err := ParseServicesXML(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("hosts = %+v (down hosts must be skipped)", hosts)
	}
	h := hosts[0]
	if h.IP.String() != "10.0.0.2" || h.OS != "Linux 4.15 - 5.19" || h.OSAccuracy != 100 || len(h.Ports) != 2 {
		t.Fatalf("host = %+v", h)
	}
	p := h.Ports[0]
	if p.Proto != "tcp" || p.Port != 53 || p.Service != "domain" || p.Product != "dnsmasq" || p.Version != "2.93" || p.Extra != "pi-hole" || p.State != "open" {
		t.Fatalf("port = %+v", p)
	}
	if hosts[1].OS != "" || len(hosts[1].Ports) != 0 {
		t.Fatalf("no-os host = %+v", hosts[1])
	}
}

func TestServiceArgs(t *testing.T) {
	got := ServiceArgs([]string{"-p", "1-65535", "-oN", "x"}, []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3")})
	for _, want := range []string{"-sV", "-O", "-Pn", "-oX", "--stats-every", "-p", "1-65535"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %s: %v", want, got)
		}
	}
	if got[len(got)-2] != "10.0.0.2" || got[len(got)-1] != "10.0.0.3" {
		t.Fatalf("targets not last: %v", got)
	}
}

func TestServicesScanNoTargets(t *testing.T) {
	ran := false
	s := NewServices(nil, func(ctx context.Context, args ...string) ([]byte, error) { ran = true; return nil, nil })
	if hs, err := s.Scan(context.Background(), nil); err != nil || hs != nil || ran {
		t.Fatalf("hs=%v err=%v ran=%v", hs, err, ran)
	}
}

func TestServicesScan(t *testing.T) {
	data, _ := os.ReadFile("testdata/services.xml")
	s := NewServices(nil, func(ctx context.Context, args ...string) ([]byte, error) { return data, nil })
	hs, err := s.Scan(context.Background(), []netip.Addr{netip.MustParseAddr("10.0.0.2")})
	if err != nil || len(hs) != 2 || s.Name() != "services" {
		t.Fatalf("hs=%v err=%v", hs, err)
	}
}
