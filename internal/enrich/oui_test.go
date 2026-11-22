package enrich

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

func TestOUIVendor(t *testing.T) {
	o, err := LoadOUI("testdata/prefixes")
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Vendor("00:0c:42:aa:bb:cc"); got != "Routerboard.com" {
		t.Fatalf("got %q", got)
	}
	if got := o.Vendor("ff:ff:ff:00:00:00"); got != "" {
		t.Fatalf("got %q", got)
	}
	var nilOUI *OUI
	if nilOUI.Vendor("00:0c:42:aa:bb:cc") != "" {
		t.Fatal("nil OUI should return empty")
	}
}

func TestEnrichKeepsExistingVendor(t *testing.T) {
	o, _ := LoadOUI("testdata/prefixes")
	obs := model.Observations{Hosts: []model.Host{
		{MAC: "00:0c:42:00:00:01", IP: netip.MustParseAddr("10.0.0.1"), Vendor: "MikroTik", Hostname: "x"},
		{MAC: "00:11:32:00:00:01", IP: netip.MustParseAddr("10.0.0.2"), Hostname: "y"},
	}}
	Enrich(&obs, o, nil)
	if obs.Hosts[0].Vendor != "MikroTik" || obs.Hosts[1].Vendor != "Synology Incorporated" {
		t.Fatalf("hosts = %+v", obs.Hosts)
	}
}

func TestResolverCachesAndTrimsDot(t *testing.T) {
	calls := 0
	r := NewResolver(time.Minute)
	r.look = func(ctx context.Context, addr string) ([]string, error) {
		calls++
		return []string{"nas.lan."}, nil
	}
	a := netip.MustParseAddr("10.0.0.5")
	if got := r.Name(context.Background(), a); got != "nas.lan" {
		t.Fatalf("got %q", got)
	}
	r.Name(context.Background(), a)
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestEnrichMarksDNSNames(t *testing.T) {
	r := NewResolver(time.Minute)
	r.look = func(ctx context.Context, addr string) ([]string, error) { return []string{"x.lan."}, nil }
	obs := model.Observations{Hosts: []model.Host{{IP: netip.MustParseAddr("10.0.0.1")}}}
	Enrich(&obs, nil, r)
	if obs.Hosts[0].Hostname != "x.lan" || !obs.Hosts[0].NameFromDNS {
		t.Fatalf("host = %+v", obs.Hosts[0])
	}
}
