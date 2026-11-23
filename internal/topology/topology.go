// Package topology builds the network map graph in Cytoscape.js element format.
package topology

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// Element is a Cytoscape.js element.
type Element struct {
	Group string         `json:"group"`
	Data  map[string]any `json:"data"`
}

// NetInfo is a configured network.
type NetInfo struct {
	Name string
	CIDR netip.Prefix
}

// Input is everything the map is built from.
type Input struct {
	Devices []model.Device
	Links   []model.Link
	// Gateways maps a network to the router ID that devices without any link attach to
	// (non-compound mode); without one a "network:<name>" node is used.
	Gateways map[string]string
	// RouterAddrs maps a configured router to the address netmon reaches it at. A router whose
	// address belongs to a scanned device (or machine) is drawn as that node.
	RouterAddrs map[string]netip.Addr
	// Machines groups device rows into one node.
	Machines map[model.MemberRef]int64
	Networks []NetInfo
	// Pings are the smoothed round-trip times of device rows.
	Pings map[model.MemberRef]model.Ping
	// Compound draws each network as a box holding its nodes; nodes in several networks sit
	// outside the boxes with a "member" edge to each.
	Compound bool
}

// group is one device node: a single device row, the rows sharing a key, or a machine.
type group struct {
	id      string
	members []model.Device
	router  bool
}

func (g *group) networks() []string {
	var out []string
	for _, d := range g.members {
		if !slices.Contains(out, d.Network) {
			out = append(out, d.Network)
		}
	}
	return out
}

// Build returns nodes and edges for the input.
func Build(in Input) []Element {
	groups := map[string]*group{}
	var order []string
	keyNode := map[string]string{}
	ipNode := map[netip.Addr]string{}
	ipHasMAC := map[netip.Addr]bool{}
	for _, d := range in.Devices {
		id := "device:" + d.Key
		if m := in.Machines[model.MemberRef{Network: d.Network, Key: d.Key}]; m != 0 {
			id = fmt.Sprintf("machine:%d", m)
		}
		g := groups[id]
		if g == nil {
			g = &group{id: id}
			groups[id] = g
			order = append(order, id)
		}
		g.members = append(g.members, d)
		if _, ok := keyNode[d.Key]; !ok {
			keyNode[d.Key] = id
		}
		if d.IP.IsValid() && (ipNode[d.IP] == "" || (d.MAC != "" && !ipHasMAC[d.IP])) {
			ipNode[d.IP] = id
			ipHasMAC[d.IP] = d.MAC != ""
		}
	}

	routers := map[string]bool{} // config-named routers
	hops := map[string]netip.Addr{}
	// resolve maps a link endpoint to a node ID; ok=false drops the endpoint.
	resolve := func(r model.NodeRef) (string, bool) {
		switch r.Kind {
		case "router":
			if ip, err := netip.ParseAddr(r.ID); err == nil {
				if id, ok := ipNode[ip]; ok {
					groups[id].router = true
					return id, true
				}
				hops["hop:"+ip.String()] = ip
				return "hop:" + ip.String(), true
			}
			if r.ID == "" {
				return "", false
			}
			if addr, ok := in.RouterAddrs[r.ID]; ok {
				if id, ok := ipNode[addr]; ok {
					groups[id].router = true
					return id, true
				}
			}
			routers[r.ID] = true
			return "router:" + r.ID, true
		case "device":
			if id, ok := keyNode[r.ID]; ok {
				return id, true
			}
			if ipStr, ok := strings.CutPrefix(r.ID, "ip:"); ok {
				if ip, err := netip.ParseAddr(ipStr); err == nil {
					if id, ok := ipNode[ip]; ok {
						return id, true
					}
				}
			}
			return "", false
		default:
			return r.Kind + ":" + r.ID, r.ID != ""
		}
	}

	type edge struct{ a, b, label, kind string }
	isRouterID := func(id string) bool {
		if g := groups[id]; g != nil {
			return g.router
		}
		return strings.HasPrefix(id, "router:") || strings.HasPrefix(id, "hop:")
	}
	resolved := make([][2]string, len(in.Links))
	okLink := make([]bool, len(in.Links))
	for i, l := range in.Links {
		a, okA := resolve(l.A)
		b, okB := resolve(l.B)
		resolved[i], okLink[i] = [2]string{a, b}, okA && okB && a != b
	}
	// Uplink ports: router ports that face another router.
	uplink := map[string]bool{}
	for i, l := range in.Links {
		if okLink[i] && l.Label != "" && isRouterID(resolved[i][0]) && isRouterID(resolved[i][1]) {
			uplink[resolved[i][0]+"|"+l.Label] = true
		}
	}

	edgesByPair := map[string]edge{}
	deviceRouters := map[string][]string{} // device node → pair keys of router→device edges
	for i, l := range in.Links {
		if !okLink[i] {
			continue
		}
		x, y := resolved[i][0], resolved[i][1]
		if isRouterID(y) && !isRouterID(x) {
			x, y = y, x
		}
		pair := x + "|" + y
		if isRouterID(x) == isRouterID(y) && y < x {
			pair = y + "|" + x
		}
		if e, ok := edgesByPair[pair]; ok {
			if e.label == "" && l.Label != "" {
				e.label = l.Label
				edgesByPair[pair] = e
			}
			continue
		}
		edgesByPair[pair] = edge{a: x, b: y, label: l.Label, kind: "link"}
		if isRouterID(x) && !isRouterID(y) {
			deviceRouters[y] = append(deviceRouters[y], pair)
		}
	}
	// A device seen by several routers keeps only the edges not on an uplink port.
	for _, pairs := range deviceRouters {
		if len(pairs) < 2 {
			continue
		}
		var access int
		for _, p := range pairs {
			e := edgesByPair[p]
			if !uplink[e.a+"|"+e.label] {
				access++
			}
		}
		if access == 0 {
			continue
		}
		for _, p := range pairs {
			if e := edgesByPair[p]; uplink[e.a+"|"+e.label] {
				delete(edgesByPair, p)
			}
		}
	}

	netOf := func(ip netip.Addr) string {
		for _, n := range in.Networks {
			if n.CIDR.Contains(ip) {
				return n.Name
			}
		}
		return ""
	}

	// A router node with an address inside a network is that network's gateway: hosts there with
	// no link of their own (one-hop traces on netmon's own LAN, nmap without traceroute) hang off it.
	inferred := map[string]string{}
	for _, id := range order {
		if !groups[id].router {
			continue
		}
		for _, d := range groups[id].members {
			if n := netOf(d.IP); d.IP.IsValid() && n != "" && inferred[n] == "" {
				inferred[n] = id
			}
		}
	}
	gatewayOf := map[string][]string{} // node → networks whose configured router it is
	// A network's configured router (Gateways) wins over the inferred one.
	for _, n := range sortedKeys(in.Gateways) {
		if name := in.Gateways[n]; name != "" {
			if id, ok := resolve(model.NodeRef{Kind: "router", ID: name}); ok {
				inferred[n] = id
				gatewayOf[id] = append(gatewayOf[id], n)
			}
		}
	}
	linked := map[string]bool{}
	for _, e := range edgesByPair {
		linked[e.a], linked[e.b] = true, true
	}
	for _, id := range order {
		nets := groups[id].networks()
		if linked[id] || len(nets) != 1 {
			continue
		}
		if gw := inferred[nets[0]]; gw != "" && gw != id {
			edgesByPair[gw+"|"+id] = edge{a: gw, b: id, kind: "link"}
			linked[id] = true
		}
	}

	var els []Element
	parents := map[string]string{} // node → parent box
	if in.Compound {
		boxes := []string{}
		cidr := map[string]string{}
		for _, n := range in.Networks {
			boxes = append(boxes, n.Name)
			cidr[n.Name] = n.CIDR.String()
		}
		for _, id := range order {
			for _, n := range groups[id].networks() {
				if !slices.Contains(boxes, n) {
					boxes = append(boxes, n)
				}
			}
		}
		for _, n := range boxes {
			label := n
			if c := cidr[n]; c != "" {
				label += "  " + c
			}
			els = append(els, Element{Group: "nodes", Data: map[string]any{"id": "net:" + n, "kind": "net", "label": label, "network": n}})
		}
		// A transit network (a VPN, a backbone) is one where at least half the nodes are
		// routers or sit in several networks. Nodes in several networks are drawn inside their
		// transit network, with member edges to their other networks.
		total, routerish := map[string]int{}, map[string]int{}
		for _, id := range order {
			nets := groups[id].networks()
			for _, n := range nets {
				total[n]++
				if len(nets) > 1 || groups[id].router {
					routerish[n]++
				}
			}
		}
		transit := func(n string) float64 {
			if total[n] == 0 {
				return 0
			}
			return float64(routerish[n]) / float64(total[n])
		}
		for _, id := range order {
			nets := groups[id].networks()
			if len(nets) == 1 {
				parents[id] = "net:" + nets[0]
				continue
			}
			best, tie := "", false
			for _, n := range nets {
				switch {
				case transit(n) < 0.5:
				case best == "" || transit(n) > transit(best):
					best, tie = n, false
				case transit(n) == transit(best):
					tie = true
				}
			}
			if tie { // ambiguous: keep it between the boxes
				best = ""
			}
			if best != "" {
				parents[id] = "net:" + best
			}
			for _, n := range nets {
				if n != best {
					edgesByPair[id+"|net:"+n] = edge{a: id, b: "net:" + n, kind: "member"}
				}
			}
		}
		// A router node that matches no device sits in the box of the one network it serves.
		served := map[string][]string{}
		for n, name := range in.Gateways {
			if routers[name] {
				served[name] = append(served[name], n)
			}
		}
		for name, nets := range served {
			if len(nets) == 1 {
				parents["router:"+name] = "net:" + nets[0]
			}
		}
		for id, ip := range hops {
			if n := netOf(ip); n != "" {
				parents[id] = "net:" + n
			}
		}
	} else {
		fallback := map[string]bool{}
		for _, id := range order {
			if linked[id] {
				continue
			}
			network := groups[id].members[0].Network
			var parent string
			if gw := in.Gateways[network]; gw != "" {
				parent = "router:" + gw
				routers[gw] = true
			} else {
				parent = "network:" + network
				if !fallback[parent] {
					fallback[parent] = true
					els = append(els, Element{Group: "nodes", Data: map[string]any{"id": parent, "kind": "network", "label": network}})
				}
			}
			edgesByPair[parent+"|"+id] = edge{a: parent, b: id, kind: "link"}
		}
	}

	withParent := func(id string, data map[string]any) map[string]any {
		if nets := gatewayOf[id]; len(nets) > 0 {
			data["gatewayOf"] = nets
		}
		if p := parents[id]; p != "" {
			data["parent"] = p
		}
		return data
	}
	for _, id := range sortedKeys(routers) {
		els = append(els, Element{Group: "nodes", Data: withParent("router:"+id, map[string]any{"id": "router:" + id, "kind": "router", "label": id})})
	}
	for _, id := range sortedKeys(hops) {
		ip := hops[id]
		els = append(els, Element{Group: "nodes", Data: withParent(id, map[string]any{
			"id": id, "kind": "hop", "label": ip.String(), "ip": ip.String(), "network": netOf(ip)})})
	}
	for _, id := range order {
		els = append(els, Element{Group: "nodes", Data: withParent(id, deviceData(groups[id], in.Pings))})
	}

	pairs := make([]string, 0, len(edgesByPair))
	for p := range edgesByPair {
		pairs = append(pairs, p)
	}
	sort.Strings(pairs)
	for _, p := range pairs {
		e := edgesByPair[p]
		els = append(els, Element{Group: "edges", Data: map[string]any{"id": "e:" + p, "source": e.a, "target": e.b,
			"label": e.label, "kind": e.kind}})
	}
	return els
}

type memberJSON struct {
	Network string `json:"network"`
	Key     string `json:"key"`
	IP      string `json:"ip"`
	MAC     string `json:"mac"`
	Online  bool   `json:"online"`
	Port    string `json:"port"`
}

// rttJSON is a device's round-trip time in one network.
type rttJSON struct {
	Ms     float64   `json:"ms"`
	At     time.Time `json:"at"`
	Origin string    `json:"origin"`
}

func deviceData(g *group, pings map[model.MemberRef]model.Ping) map[string]any {
	first := g.members[0]
	var label, hostname, vendor, port string
	var ips, macs []string
	var members []memberJSON
	rtt := map[string]rttJSON{}
	online, known := false, true
	var firstSeen, lastSeen time.Time
	for _, d := range g.members {
		label = cmpOr(label, d.Label)
		hostname = cmpOr(hostname, d.Hostname)
		vendor = cmpOr(vendor, d.Vendor)
		port = cmpOr(port, d.Port)
		ip := ""
		if d.IP.IsValid() {
			ip = d.IP.String()
			if !slices.Contains(ips, ip) {
				ips = append(ips, ip)
			}
		}
		if d.MAC != "" && !slices.Contains(macs, d.MAC) {
			macs = append(macs, d.MAC)
		}
		online = online || d.Online
		known = known && d.Known
		if firstSeen.IsZero() || d.FirstSeen.Before(firstSeen) {
			firstSeen = d.FirstSeen
		}
		if d.LastSeen.After(lastSeen) {
			lastSeen = d.LastSeen
		}
		members = append(members, memberJSON{d.Network, d.Key, ip, d.MAC, d.Online, d.Port})
		if p := pings[model.MemberRef{Network: d.Network, Key: d.Key}]; p.OK {
			rtt[d.Network] = rttJSON{float64(p.RTT.Microseconds()) / 1000, p.At, p.Origin}
		}
	}
	ip := strings.Join(ips, ", ")
	status := "offline"
	if online {
		status = "online"
	}
	nets := g.networks()
	data := map[string]any{
		"id": g.id, "key": first.Key, "kind": "device", "label": cmpOr(label, hostname, first.IP.String()),
		"status": status, "known": known, "ip": ip, "mac": strings.Join(macs, ", "), "vendor": vendor,
		"hostname": hostname, "network": strings.Join(nets, ", "), "networks": nets, "port": port,
		"lastSeen": lastSeen, "firstSeen": firstSeen, "members": members,
	}
	if !first.IP.IsValid() && label == "" && hostname == "" {
		data["label"] = first.Key
	}
	if len(rtt) > 0 {
		data["rtt"] = rtt
	}
	if g.router {
		data["router"] = true
	}
	if strings.HasPrefix(g.id, "machine:") {
		data["machine"] = strings.TrimPrefix(g.id, "machine:")
	}
	return data
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
