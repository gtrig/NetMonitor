package topology

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

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
	e := edges(els)
	_, fwd := e["hop:10.0.10.1->device:"+gw.Key]
	_, back := e["device:"+gw.Key+"->hop:10.0.10.1"]
	if !fwd && !back {
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
	if _, ok := e["router:core->device:"+c.Key]; !ok || n["router:core"]["parent"] != "net:ATLan" {
		t.Fatalf("unlinked host should hang off its network's router: %v", e)
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

// Hosts on netmon's own LAN have one-hop traces (no router in them): they attach to the router
// node that has an address in their network.
func TestUnlinkedHostsAttachToInferredGateway(t *testing.T) {
	gwLAN := deviceIn("GTLan", "mac:aa:00:00:00:00:01", "192.168.8.1")
	gwVPN := deviceIn("VPN", "ip:10.0.10.5", "10.0.10.5")
	host := deviceIn("GTLan", "mac:bb:00:00:00:00:02", "192.168.8.50")
	in := Input{Devices: []model.Device{gwLAN, gwVPN, host}, Networks: nets, Compound: true,
		Machines: map[model.MemberRef]int64{{Network: "GTLan", Key: gwLAN.Key}: 2, {Network: "VPN", Key: gwVPN.Key}: 2},
		Links:    []model.Link{{A: router("192.168.8.1"), B: router("10.0.10.1")}}}
	for _, compound := range []bool{true, false} {
		in.Compound = compound
		e := edges(Build(in))
		if _, ok := e["machine:2->device:"+host.Key]; !ok {
			t.Fatalf("compound=%v: host not attached to gateway: %v", compound, e)
		}
	}
}

// Routers that join their LANs over a shared network (the VPN) are drawn inside that network,
// with a member edge to their own LAN.
func TestMultiNetworkNodesGoIntoSharedNetwork(t *testing.T) {
	gt := deviceIn("GTLan", "mac:aa:00:00:00:00:01", "192.168.8.1")
	gtv := deviceIn("VPN", "ip:10.0.10.5", "10.0.10.5")
	at := deviceIn("ATLan", "ip:192.168.88.1", "192.168.88.1")
	atv := deviceIn("VPN", "ip:10.0.10.6", "10.0.10.6")
	lone := deviceIn("GTLan", "mac:cc:00:00:00:00:03", "192.168.8.9")
	lonev := deviceIn("ATLan", "ip:192.168.88.9", "192.168.88.9")
	devs := []model.Device{gt, gtv, at, atv, lone, lonev}
	for i := 20; i < 23; i++ { // ordinary hosts: LANs are not transit networks
		devs = append(devs, deviceIn("GTLan", fmt.Sprintf("ip:192.168.8.%d", i), fmt.Sprintf("192.168.8.%d", i)),
			deviceIn("ATLan", fmt.Sprintf("ip:192.168.88.%d", i), fmt.Sprintf("192.168.88.%d", i)))
	}
	els := Build(Input{Devices: devs, Networks: nets, Compound: true,
		Machines: map[model.MemberRef]int64{
			{Network: "GTLan", Key: gt.Key}: 1, {Network: "VPN", Key: gtv.Key}: 1,
			{Network: "ATLan", Key: at.Key}: 2, {Network: "VPN", Key: atv.Key}: 2,
			{Network: "GTLan", Key: lone.Key}: 3, {Network: "ATLan", Key: lonev.Key}: 3,
		}})
	n, e := nodes(els), edges(els)
	if n["machine:1"]["parent"] != "net:VPN" || n["machine:2"]["parent"] != "net:VPN" {
		t.Fatalf("routers not in VPN: %v / %v", n["machine:1"], n["machine:2"])
	}
	if _, ok := e["machine:1->net:VPN"]; ok {
		t.Fatal("member edge to own parent box")
	}
	if _, ok := e["machine:1->net:GTLan"]; !ok {
		t.Fatalf("lost member edge to own LAN: %v", e)
	}
	// machine 3 shares no network with another multi-network node: stays outside the boxes.
	if _, ok := n["machine:3"]["parent"]; ok {
		t.Fatalf("machine 3 = %v", n["machine:3"])
	}
}

// A configured router whose address belongs to a scanned device (or machine) is that node,
// not a second one; its own ARP entry for that device does not become a self-loop.
func TestConfiguredRouterMergesWithItsDevice(t *testing.T) {
	devs := []model.Device{
		deviceIn("ATLan", "ip:192.168.88.1", "192.168.88.1"),
		deviceIn("ATLan", "mac:aa:00:00:00:00:02", "192.168.88.2"),
		deviceIn("VPN", "ip:10.0.10.6", "10.0.10.6"),
		deviceIn("ATLan", "mac:aa:00:00:00:00:10", "192.168.88.10"),
	}
	machines := map[model.MemberRef]int64{
		{Network: "ATLan", Key: "ip:192.168.88.1"}:       1,
		{Network: "ATLan", Key: "mac:aa:00:00:00:00:02"}: 1,
		{Network: "VPN", Key: "ip:10.0.10.6"}:            1,
	}
	els := Build(Input{Devices: devs, Machines: machines, Compound: true,
		Networks:    []NetInfo{{"ATLan", netip.MustParsePrefix("192.168.88.0/24")}, {"VPN", netip.MustParsePrefix("10.0.10.0/24")}},
		Gateways:    map[string]string{"ATLan": "ATHome"},
		RouterAddrs: map[string]netip.Addr{"ATHome": netip.MustParseAddr("192.168.88.1")},
		Links: []model.Link{
			{A: router("ATHome"), B: devRef("mac:aa:00:00:00:00:10")},
			{A: router("ATHome"), B: devRef("mac:aa:00:00:00:00:02")},
		}})
	n, e := nodes(els), edges(els)
	if n["router:ATHome"] != nil {
		t.Fatalf("duplicate router node: %v", n["router:ATHome"])
	}
	if n["machine:1"]["router"] != true {
		t.Fatalf("machine not marked router: %v", n["machine:1"])
	}
	if _, ok := e["machine:1->device:mac:aa:00:00:00:00:10"]; !ok {
		t.Fatalf("edges = %v", e)
	}
	for k := range e {
		if k == "machine:1->machine:1" {
			t.Fatal("self loop")
		}
	}
}

// In the all-networks view, hosts with no link hang off the network's configured router too,
// and a router node that matches no device sits in the box of the network it serves.
func TestCompoundUnlinkedHostsAttachToConfiguredRouter(t *testing.T) {
	devs := []model.Device{
		deviceIn("ATLan", "mac:aa:00:00:00:00:10", "192.168.88.10"),
		deviceIn("ATLan", "ip:192.168.88.126", "192.168.88.126"),
		deviceIn("VPN", "ip:10.0.10.1", "10.0.10.1"),
	}
	els := Build(Input{Devices: devs, Compound: true,
		Networks: []NetInfo{{"ATLan", netip.MustParsePrefix("192.168.88.0/24")}, {"VPN", netip.MustParsePrefix("10.0.10.0/24")}},
		Gateways: map[string]string{"ATLan": "ATHome"},
		Links:    []model.Link{{A: router("ATHome"), B: devRef("mac:aa:00:00:00:00:10")}}})
	n, e := nodes(els), edges(els)
	if _, ok := e["router:ATHome->device:ip:192.168.88.126"]; !ok {
		t.Fatalf("unlinked host not attached: %v", e)
	}
	if n["router:ATHome"]["parent"] != "net:ATLan" {
		t.Fatalf("router parent = %v", n["router:ATHome"]["parent"])
	}
}

func TestBuildRTTPerNetwork(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	a, b := deviceIn("lan", "ip:10.0.0.5", "10.0.0.5"), deviceIn("vpn", "ip:10.9.0.5", "10.9.0.5")
	a.Port = "ether2"
	refA, refB := model.MemberRef{Network: "lan", Key: a.Key}, model.MemberRef{Network: "vpn", Key: b.Key}
	els := Build(Input{Devices: []model.Device{a, b}, Machines: map[model.MemberRef]int64{refA: 1, refB: 1},
		Pings: map[model.MemberRef]model.Ping{refA: {RTT: 1500 * time.Microsecond, OK: true, At: at, Origin: "gw"},
			refB: {Origin: "netmon"}}})
	data := nodes(els)["machine:1"]
	rtt, _ := data["rtt"].(map[string]rttJSON)
	if len(rtt) != 1 || rtt["lan"] != (rttJSON{Ms: 1.5, At: at, Origin: "gw"}) {
		t.Fatalf("rtt = %+v", data["rtt"])
	}
	if m := data["members"].([]memberJSON); m[0].Port != "ether2" || m[1].Port != "" {
		t.Fatalf("members = %+v", m)
	}
}

func TestBuildNoRTTWhenMissing(t *testing.T) {
	els := Build(Input{Devices: []model.Device{device("ip:10.0.0.5", "10.0.0.5")}})
	if _, ok := nodes(els)["device:ip:10.0.0.5"]["rtt"]; ok {
		t.Fatal("rtt present")
	}
}

func TestBuildGatewayOf(t *testing.T) {
	gw := device("ip:10.0.0.1", "10.0.0.1")
	host := device("ip:10.0.0.5", "10.0.0.5")
	addr := map[string]netip.Addr{"core": gw.IP}
	base := Input{Devices: []model.Device{gw, host}, Gateways: map[string]string{"lan": "core"},
		Networks: []NetInfo{{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24")}}}
	check := func(name, id string, in Input) {
		t.Helper()
		for _, compound := range []bool{false, true} {
			in.Compound = compound
			got, _ := nodes(Build(in))[id]["gatewayOf"].([]string)
			if len(got) != 1 || got[0] != "lan" {
				t.Errorf("%s (compound %v): gatewayOf = %v", name, compound, got)
			}
		}
	}
	dev := base
	dev.RouterAddrs = addr
	check("device", "device:ip:10.0.0.1", dev)
	mach := dev
	mach.Machines = map[model.MemberRef]int64{{Network: "lan", Key: gw.Key}: 7}
	check("machine", "machine:7", mach)
	check("bare router", "router:core", base)
}
