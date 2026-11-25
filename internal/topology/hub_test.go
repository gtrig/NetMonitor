package topology

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/gtrig/netmon/internal/model"
)

// vpnInput is a VPN between two LANs as traced from inside Docker: the VPN server answers
// traceroute from its Docker address.
func vpnInput(hub string) Input {
	ref := func(n, ip string) model.MemberRef { return model.MemberRef{Network: n, Key: "ip:" + ip} }
	var devs []model.Device
	for _, d := range [][2]string{{"AT", "192.168.88.1"}, {"AT", "192.168.88.9"}, {"AT", "192.168.88.10"}, {"GT", "192.168.8.1"}, {"GT", "192.168.8.10"}, {"GT", "192.168.8.9"},
		{"VPN", "10.0.10.1"}, {"VPN", "10.0.10.4"}, {"VPN", "10.0.10.5"}, {"VPN", "10.0.10.6"}} {
		devs = append(devs, deviceIn(d[0], "ip:"+d[1], d[1]))
	}
	in := Input{Compound: true, Devices: devs,
		Links: []model.Link{
			{Network: "VPN", A: router("172.19.0.1"), B: devRef("ip:10.0.10.1")},
			{Network: "VPN", A: router("172.19.0.1"), B: router("172.18.0.2")},
			{Network: "VPN", A: router("172.18.0.2"), B: devRef("ip:10.0.10.5")},
			{Network: "VPN", A: router("172.18.0.2"), B: devRef("ip:10.0.10.6")},
		},
		Machines: map[model.MemberRef]int64{ref("AT", "192.168.88.1"): 1, ref("VPN", "10.0.10.6"): 1,
			ref("GT", "192.168.8.1"): 2, ref("VPN", "10.0.10.5"): 2},
		Gateways:    map[string]string{"AT": "at", "GT": "gt"},
		RouterAddrs: map[string]netip.Addr{"at": netip.MustParseAddr("192.168.88.1"), "gt": netip.MustParseAddr("192.168.8.1")},
		Networks: []NetInfo{{Name: "AT", CIDR: netip.MustParsePrefix("192.168.88.0/24")},
			{Name: "GT", CIDR: netip.MustParsePrefix("192.168.8.0/24")}, {Name: "VPN", CIDR: netip.MustParsePrefix("10.0.10.0/24")}}}
	if hub != "" {
		in.Networks[2].Hub = netip.MustParseAddr(hub)
	}
	return in
}

func edgeList(els []Element) []string {
	var out []string
	for e := range edges(els) {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
}

func TestHub(t *testing.T) {
	els := Build(vpnInput("10.0.10.1"))
	want := []string{
		"device:ip:10.0.10.1->device:ip:10.0.10.4", "device:ip:10.0.10.1->machine:1", "device:ip:10.0.10.1->machine:2",
		"machine:1->device:ip:192.168.88.10", "machine:1->device:ip:192.168.88.9", "machine:1->net:AT",
		"machine:2->device:ip:192.168.8.10", "machine:2->device:ip:192.168.8.9", "machine:2->net:GT"}
	if got := edgeList(els); !slices.Equal(got, want) {
		t.Errorf("edges = %q", got)
	}
	ns := nodes(els)
	for id := range ns {
		if len(id) > 4 && id[:4] == "hop:" {
			t.Errorf("hop left: %s", id)
		}
	}
	h := ns["device:ip:10.0.10.1"]
	if hubOf, _ := h["hubOf"].([]string); !slices.Equal(hubOf, []string{"VPN"}) || h["router"] != true || h["parent"] != "net:VPN" {
		t.Errorf("hub = %v", h)
	}
	if ns["machine:1"]["parent"] != "net:VPN" || ns["machine:2"]["parent"] != "net:VPN" {
		t.Errorf("machines moved: %v %v", ns["machine:1"]["parent"], ns["machine:2"]["parent"])
	}
}

func TestHubNotSeenChangesNothing(t *testing.T) {
	want := edgeList(Build(vpnInput("")))
	if got := edgeList(Build(vpnInput("10.0.10.99"))); !slices.Equal(got, want) {
		t.Errorf("edges = %q, want %q", got, want)
	}
}

func TestHubKeepsOtherNetworksHops(t *testing.T) {
	in := vpnInput("10.0.10.1")
	in.Links = append(in.Links,
		model.Link{Network: "GT", A: router("172.30.0.1"), B: devRef("ip:192.168.8.9")},  // another network's trace
		model.Link{Network: "VPN", A: router("10.0.10.200"), B: devRef("ip:10.0.10.4")},  // inside a monitored network
		model.Link{Network: "VPN", A: devRef("ip:10.0.10.4"), B: devRef("ip:10.0.10.1")}) // same pair, other way round
	e := edges(Build(in))
	for _, k := range []string{"hop:172.30.0.1->device:ip:192.168.8.9", "hop:10.0.10.200->device:ip:10.0.10.4"} {
		if _, ok := e[k]; !ok {
			t.Errorf("%s missing: %v", k, e)
		}
	}
	n := 0
	for k := range e {
		if k == "device:ip:10.0.10.1->device:ip:10.0.10.4" || k == "device:ip:10.0.10.4->device:ip:10.0.10.1" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d lines between hub and .4: %v", n, e)
	}
}

// A hub that is itself a machine in several networks sits in the network it is the hub of, and a
// member of a network with a hub is tied to it by the hub's line alone.
func TestHubMachine(t *testing.T) {
	els := Build(vpnInput("10.0.10.5"))
	if p := nodes(els)["machine:2"]["parent"]; p != "net:VPN" {
		t.Errorf("parent = %v", p)
	}
	e := edges(els)
	for _, k := range []string{"machine:2->machine:1", "machine:2->device:ip:10.0.10.1", "machine:2->net:GT"} {
		if _, ok := e[k]; !ok {
			if k != "machine:2->machine:1" {
				t.Errorf("%s missing: %v", k, e)
			} else if _, ok := e["machine:1->machine:2"]; !ok {
				t.Errorf("%s missing: %v", k, e)
			}
		}
	}
	for k := range e {
		if k == "machine:1->net:VPN" || k == "machine:2->net:VPN" {
			t.Errorf("extra line %s", k)
		}
	}
}

func TestHubSingleNetworkView(t *testing.T) {
	in := vpnInput("10.0.10.1")
	in.Compound = false
	in.Devices = slices.DeleteFunc(in.Devices, func(d model.Device) bool { return d.Network != "VPN" })
	if got := edgeList(Build(in)); len(got) != 3 {
		t.Errorf("edges = %q", got)
	}
}
