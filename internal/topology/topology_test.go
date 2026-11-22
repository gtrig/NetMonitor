package topology

import (
	"net/netip"
	"testing"

	"github.com/gtrig/netmon/internal/model"
)

func router(id string) model.NodeRef  { return model.NodeRef{Kind: "router", ID: id} }
func devRef(key string) model.NodeRef { return model.NodeRef{Kind: "device", ID: key} }

func device(key, ip string) model.Device {
	return model.Device{Key: key, Network: "lan", IP: netip.MustParseAddr(ip), Online: true}
}

func deviceIn(network, key, ip string) model.Device {
	d := device(key, ip)
	d.Network = network
	return d
}

func edges(els []Element) map[string]string {
	out := map[string]string{}
	for _, e := range els {
		if e.Group == "edges" {
			l, _ := e.Data["label"].(string)
			out[e.Data["source"].(string)+"->"+e.Data["target"].(string)] = l
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
	els := Build(Input{Links: []model.Link{{A: router("core"), B: router("ap"), Label: "ether5"}}})
	n := nodes(els)
	if n["router:core"]["kind"] != "router" || n["router:ap"] == nil {
		t.Fatalf("nodes = %v", n)
	}
	if edges(els)["router:core->router:ap"] != "ether5" {
		t.Fatalf("edges = %v", edges(els))
	}
}

func TestBuildOrphanAttachesToGateway(t *testing.T) {
	els := Build(Input{Devices: []model.Device{device("ip:10.0.0.5", "10.0.0.5")}, Gateways: map[string]string{"lan": "core"}})
	if _, ok := edges(els)["router:core->device:ip:10.0.0.5"]; !ok {
		t.Fatalf("edges = %v", edges(els))
	}
	// No gateway configured: synthetic network node.
	els = Build(Input{Devices: []model.Device{device("ip:10.0.0.5", "10.0.0.5")}})
	if nodes(els)["network:lan"]["kind"] != "network" {
		t.Fatalf("nodes = %v", nodes(els))
	}
	if _, ok := edges(els)["network:lan->device:ip:10.0.0.5"]; !ok {
		t.Fatalf("edges = %v", edges(els))
	}
}

func TestBuildDedupesBidirectionalLinks(t *testing.T) {
	els := Build(Input{Links: []model.Link{
		{A: router("core"), B: router("ap"), Label: "ether5", Source: "router:core"},
		{A: router("ap"), B: router("core"), Label: "ether1", Source: "router:ap"},
		{A: router("core"), B: router("ap"), Source: "snmp:core"},
	}})
	if len(edges(els)) != 1 {
		t.Fatalf("edges = %v", edges(els))
	}
}

func TestBuildPrefersAccessPortOverUplink(t *testing.T) {
	// Device behind ap is seen by core on its uplink port ether5 and by ap on ether3.
	els := Build(Input{Devices: []model.Device{device("mac:aa:00:00:00:00:01", "10.0.0.7")}, Links: []model.Link{
		{A: router("core"), B: router("ap"), Label: "ether5"},
		{A: router("core"), B: devRef("mac:aa:00:00:00:00:01"), Label: "ether5"},
		{A: router("ap"), B: devRef("mac:aa:00:00:00:00:01"), Label: "ether3"},
	}})
	e := edges(els)
	if _, ok := e["router:core->device:mac:aa:00:00:00:00:01"]; ok {
		t.Fatalf("uplink edge kept: %v", e)
	}
	if e["router:ap->device:mac:aa:00:00:00:00:01"] != "ether3" {
		t.Fatalf("edges = %v", e)
	}
}

func TestBuildMapsIPLinkToDevice(t *testing.T) {
	els := Build(Input{Devices: []model.Device{device("mac:aa:00:00:00:00:01", "10.0.0.7")},
		Links: []model.Link{{A: router("10.0.0.1"), B: devRef("ip:10.0.0.7")}}})
	if _, ok := edges(els)["hop:10.0.0.1->device:mac:aa:00:00:00:00:01"]; !ok {
		t.Fatalf("edges = %v", edges(els))
	}
	if _, ok := nodes(els)["device:ip:10.0.0.7"]; ok {
		t.Fatal("phantom device node created")
	}
}

func TestBuildDeviceNodeData(t *testing.T) {
	d := device("mac:aa:00:00:00:00:01", "10.0.0.7")
	d.Hostname, d.Online, d.Known = "nas", false, true
	n := nodes(Build(Input{Devices: []model.Device{d}}))["device:mac:aa:00:00:00:00:01"]
	if n["label"] != "nas" || n["status"] != "offline" || n["known"] != true || n["ip"] != "10.0.0.7" {
		t.Fatalf("node = %v", n)
	}
}

var nets = []NetInfo{
	{Name: "GTLan", CIDR: netip.MustParsePrefix("192.168.8.0/24")},
	{Name: "VPN", CIDR: netip.MustParsePrefix("10.0.10.0/24")},
	{Name: "ATLan", CIDR: netip.MustParsePrefix("192.168.88.0/24")},
}

func TestMachineIsOneNode(t *testing.T) {
	a := deviceIn("GTLan", "mac:aa:00:00:00:00:01", "192.168.8.1")
	b := deviceIn("VPN", "ip:10.0.10.5", "10.0.10.5")
	b.Online = false
	els := Build(Input{Devices: []model.Device{a, b}, Networks: nets, Machines: map[model.MemberRef]int64{
		{Network: "GTLan", Key: a.Key}: 1, {Network: "VPN", Key: b.Key}: 1}})
	n := nodes(els)
	m := n["machine:1"]
	if m == nil || n["device:"+a.Key] != nil || n["device:"+b.Key] != nil {
		t.Fatalf("nodes = %v", n)
	}
	if m["ip"] != "192.168.8.1, 10.0.10.5" || len(m["networks"].([]string)) != 2 || m["status"] != "online" {
		t.Fatalf("machine = %v", m)
	}
}

func TestHopResolvesToDevice(t *testing.T) {
	gw := deviceIn("GTLan", "mac:aa:00:00:00:00:01", "192.168.8.1")
	els := Build(Input{Devices: []model.Device{gw}, Networks: nets,
		Links: []model.Link{{A: router("192.168.8.1"), B: router("10.0.10.1")}}})
	n := nodes(els)
	if n["router:192.168.8.1"] != nil || n["hop:192.168.8.1"] != nil {
		t.Fatalf("hop not resolved to device: %v", n)
	}
	if n["device:"+gw.Key]["router"] != true {
		t.Fatalf("device not flagged as router: %v", n["device:"+gw.Key])
	}
	if _, ok := edges(els)["hop:10.0.10.1->device:"+gw.Key]; !ok {
		t.Fatalf("edges = %v", edges(els))
	}
}

func TestUnknownHopBecomesHopNodeInNetwork(t *testing.T) {
	els := Build(Input{Networks: nets, Compound: true,
		Links: []model.Link{{A: router("10.0.10.1"), B: router("172.16.0.1")}}})
	n := nodes(els)
	if n["hop:10.0.10.1"]["kind"] != "hop" || n["hop:10.0.10.1"]["parent"] != "net:VPN" {
		t.Fatalf("hop in VPN = %v", n["hop:10.0.10.1"])
	}
	if _, ok := n["hop:172.16.0.1"]["parent"]; ok || n["hop:172.16.0.1"] == nil {
		t.Fatalf("hop outside networks = %v", n["hop:172.16.0.1"])
	}
}

func TestCompoundParentsAndMemberEdges(t *testing.T) {
	a := deviceIn("GTLan", "mac:aa:00:00:00:00:01", "192.168.8.1")
	b := deviceIn("VPN", "ip:10.0.10.5", "10.0.10.5")
	c := deviceIn("ATLan", "ip:192.168.88.20", "192.168.88.20")
	els := Build(Input{Devices: []model.Device{a, b, c}, Networks: nets, Compound: true,
		Gateways: map[string]string{"ATLan": "core"},
		Machines: map[model.MemberRef]int64{{Network: "GTLan", Key: a.Key}: 3, {Network: "VPN", Key: b.Key}: 3}})
	n := nodes(els)
	for _, net := range []string{"net:GTLan", "net:VPN", "net:ATLan"} {
		if n[net]["kind"] != "net" {
			t.Fatalf("missing box %s: %v", net, n)
		}
	}
	if n["device:"+c.Key]["parent"] != "net:ATLan" {
		t.Fatalf("single-network device = %v", n["device:"+c.Key])
	}
	if _, ok := n["machine:3"]["parent"]; ok {
		t.Fatalf("multi-network machine has a parent: %v", n["machine:3"])
	}
	e := edges(els)
	_, toGT := e["machine:3->net:GTLan"]
	_, toVPN := e["machine:3->net:VPN"]
	if !toGT || !toVPN {
		t.Fatalf("member edges = %v", e)
	}
	if _, ok := e["router:core->device:"+c.Key]; ok || n["router:core"] != nil {
		t.Fatalf("gateway fallback used in compound mode: %v", e)
	}
}

func TestSameKeyAcrossNetworksSharesNode(t *testing.T) {
	a := deviceIn("GTLan", "mac:aa:00:00:00:00:01", "192.168.8.5")
	b := deviceIn("VPN", "mac:aa:00:00:00:00:01", "10.0.10.5")
	n := nodes(Build(Input{Devices: []model.Device{a, b}, Networks: nets, Compound: true}))
	d := n["device:mac:aa:00:00:00:00:01"]
	if d == nil || len(d["networks"].([]string)) != 2 {
		t.Fatalf("node = %v", d)
	}
}
