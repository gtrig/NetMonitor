package source

import (
	"net/netip"
	"testing"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

var lan = config.Network{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24"),
	Exclude: []netip.Addr{netip.MustParseAddr("10.0.0.254")}}

func h(mac, ip, name, port string) model.Host {
	return model.Host{MAC: mac, IP: netip.MustParseAddr(ip), Hostname: name, Port: port}
}

func TestMergeFiltersOutsideCIDRAndExcluded(t *testing.T) {
	o := Merge(lan, model.Observations{Hosts: []model.Host{
		h("aa:00:00:00:00:01", "10.0.0.1", "", ""),
		h("aa:00:00:00:00:02", "10.0.1.1", "", ""),
		h("aa:00:00:00:00:03", "10.0.0.254", "", ""),
	}})
	if len(o.Hosts) != 1 || o.Hosts[0].IP.String() != "10.0.0.1" {
		t.Fatalf("hosts = %+v", o.Hosts)
	}
}

func TestMergeAttachesIPOnlyHostToMACDevice(t *testing.T) {
	o := Merge(lan, model.Observations{Hosts: []model.Host{
		h("", "10.0.0.5", "nas", ""),
		h("AA-00-00-00-00-05", "10.0.0.5", "", "ether2"),
	}})
	if len(o.Hosts) != 1 {
		t.Fatalf("hosts = %+v", o.Hosts)
	}
	got := o.Hosts[0]
	if got.MAC != "aa:00:00:00:00:05" || got.Hostname != "nas" || got.Port != "ether2" {
		t.Fatalf("merged = %+v", got)
	}
}

func TestMergeCollapsesDuplicates(t *testing.T) {
	o := Merge(lan, model.Observations{Hosts: []model.Host{
		h("aa:00:00:00:00:01", "10.0.0.1", "", ""),
		h("aa:00:00:00:00:01", "10.0.0.1", "printer", "wlan1"),
	}})
	if len(o.Hosts) != 1 || o.Hosts[0].Hostname != "printer" || o.Hosts[0].Port != "wlan1" {
		t.Fatalf("hosts = %+v", o.Hosts)
	}
}

func TestMergeDropsLinksToFilteredDevices(t *testing.T) {
	o := Merge(lan, model.Observations{
		Hosts: []model.Host{h("aa:00:00:00:00:02", "10.0.1.1", "", "")},
		Links: []model.Link{
			{A: model.NodeRef{Kind: "router", ID: "core"}, B: model.NodeRef{Kind: "device", ID: "mac:aa:00:00:00:00:02"}},
			{A: model.NodeRef{Kind: "router", ID: "core"}, B: model.NodeRef{Kind: "router", ID: "ap"}},
		},
	})
	if len(o.Links) != 1 || o.Links[0].B.ID != "ap" {
		t.Fatalf("links = %+v", o.Links)
	}
}
