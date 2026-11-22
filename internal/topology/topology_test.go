package topology

import (
	"net/netip"
	"testing"

	"github.com/gtrig/netmon/internal/model"
)

func router(id string) model.NodeRef { return model.NodeRef{Kind: "router", ID: id} }
func devRef(key string) model.NodeRef { return model.NodeRef{Kind: "device", ID: key} }

func device(key, ip string) model.Device {
	return model.Device{Key: key, Network: "lan", IP: netip.MustParseAddr(ip), Online: true}
}

func edges(els []Element) map[string]string {
	out := map[string]string{}
	for _, e := range els {
		if e.Group == "edges" {
			out[e.Data["source"].(string)+"->"+e.Data["target"].(string)] = e.Data["label"].(string)
		}
	}
	return out
}

func nodes(els []Element) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, e := range els {
		if e.Group == "nodes" {
			out[e.Data["id"].(string)] = e.Data
		}
	}
	return out
}

func TestBuildRouterNeighborEdge(t *testing.T) {
	els := Build(nil, []model.Link{{A: router("core"), B: router("ap"), Label: "ether5"}}, nil)
	n := nodes(els)
	if n["router:core"]["kind"] != "router" || n["router:ap"] == nil {
		t.Fatalf("nodes = %v", n)
	}
	if edges(els)["router:core->router:ap"] != "ether5" {
		t.Fatalf("edges = %v", edges(els))
	}
}

func TestBuildOrphanAttachesToGateway(t *testing.T) {
	els := Build([]model.Device{device("ip:10.0.0.5", "10.0.0.5")}, nil, map[string]string{"lan": "core"})
	if _, ok := edges(els)["router:core->device:ip:10.0.0.5"]; !ok {
		t.Fatalf("edges = %v", edges(els))
	}
	// No gateway configured: synthetic network node.
	els = Build([]model.Device{device("ip:10.0.0.5", "10.0.0.5")}, nil, nil)
	if nodes(els)["network:lan"]["kind"] != "network" {
		t.Fatalf("nodes = %v", nodes(els))
	}
	if _, ok := edges(els)["network:lan->device:ip:10.0.0.5"]; !ok {
		t.Fatalf("edges = %v", edges(els))
	}
}

func TestBuildDedupesBidirectionalLinks(t *testing.T) {
	els := Build(nil, []model.Link{
		{A: router("core"), B: router("ap"), Label: "ether5", Source: "router:core"},
		{A: router("ap"), B: router("core"), Label: "ether1", Source: "router:ap"},
		{A: router("core"), B: router("ap"), Source: "snmp:core"},
	}, nil)
	if len(edges(els)) != 1 {
		t.Fatalf("edges = %v", edges(els))
	}
}

func TestBuildPrefersAccessPortOverUplink(t *testing.T) {
	// Device behind ap is seen by core on its uplink port ether5 and by ap on ether3.
	els := Build([]model.Device{device("mac:aa:00:00:00:00:01", "10.0.0.7")}, []model.Link{
		{A: router("core"), B: router("ap"), Label: "ether5"},
		{A: router("core"), B: devRef("mac:aa:00:00:00:00:01"), Label: "ether5"},
		{A: router("ap"), B: devRef("mac:aa:00:00:00:00:01"), Label: "ether3"},
	}, nil)
	e := edges(els)
	if _, ok := e["router:core->device:mac:aa:00:00:00:00:01"]; ok {
		t.Fatalf("uplink edge kept: %v", e)
	}
	if e["router:ap->device:mac:aa:00:00:00:00:01"] != "ether3" {
		t.Fatalf("edges = %v", e)
	}
}

func TestBuildMapsIPLinkToDevice(t *testing.T) {
	els := Build([]model.Device{device("mac:aa:00:00:00:00:01", "10.0.0.7")},
		[]model.Link{{A: router("10.0.0.1"), B: devRef("ip:10.0.0.7")}}, nil)
	if _, ok := edges(els)["router:10.0.0.1->device:mac:aa:00:00:00:00:01"]; !ok {
		t.Fatalf("edges = %v", edges(els))
	}
	if _, ok := nodes(els)["device:ip:10.0.0.7"]; ok {
		t.Fatal("phantom device node created")
	}
}

func TestBuildDeviceNodeData(t *testing.T) {
	d := device("mac:aa:00:00:00:00:01", "10.0.0.7")
	d.Hostname, d.Online, d.Known = "nas", false, true
	n := nodes(Build([]model.Device{d}, nil, nil))["device:mac:aa:00:00:00:00:01"]
	if n["label"] != "nas" || n["status"] != "offline" || n["known"] != true || n["ip"] != "10.0.0.7" {
		t.Fatalf("node = %v", n)
	}
}
