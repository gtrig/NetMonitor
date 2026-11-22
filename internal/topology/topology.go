// Package topology builds the network map graph in Cytoscape.js element format.
package topology

import (
	"net/netip"
	"sort"
	"strings"

	"github.com/gtrig/netmon/internal/model"
)

// Element is a Cytoscape.js element.
type Element struct {
	Group string         `json:"group"`
	Data  map[string]any `json:"data"`
}

func nodeID(r model.NodeRef) string { return r.Kind + ":" + r.ID }

// Build returns nodes and edges for devices and links. gateways maps a network name to the
// router ID that devices without any link attach to; without one a "network:<name>" node is used.
func Build(devices []model.Device, links []model.Link, gateways map[string]string) []Element {
	byKey := map[string]model.Device{}
	byIP := map[netip.Addr]string{}
	for _, d := range devices {
		byKey[d.Key] = d
		if d.IP.IsValid() {
			if _, taken := byIP[d.IP]; !taken || d.MAC != "" {
				byIP[d.IP] = d.Key
			}
		}
	}
	// resolve maps link endpoints onto known devices; ok=false drops the endpoint.
	resolve := func(r model.NodeRef) (model.NodeRef, bool) {
		if r.Kind != "device" {
			return r, r.ID != ""
		}
		if _, ok := byKey[r.ID]; ok {
			return r, true
		}
		if ipStr, ok := strings.CutPrefix(r.ID, "ip:"); ok {
			if ip, err := netip.ParseAddr(ipStr); err == nil {
				if k, ok := byIP[ip]; ok {
					return model.NodeRef{Kind: "device", ID: k}, true
				}
			}
		}
		return r, false
	}

	// Uplink ports: router ports that face another router.
	uplink := map[string]bool{}
	for _, l := range links {
		if l.A.Kind == "router" && l.B.Kind == "router" && l.Label != "" {
			uplink[nodeID(l.A)+"|"+l.Label] = true
		}
	}

	type edge struct{ a, b, label string }
	edgesByPair := map[string]edge{}
	deviceRouters := map[string][]string{} // device node → pair keys of router→device edges
	routers := map[string]bool{}
	for _, l := range links {
		a, okA := resolve(l.A)
		b, okB := resolve(l.B)
		if !okA || !okB || nodeID(a) == nodeID(b) {
			continue
		}
		if b.Kind == "router" && a.Kind == "device" {
			a, b = b, a
		}
		for _, r := range []model.NodeRef{a, b} {
			if r.Kind == "router" {
				routers[r.ID] = true
			}
		}
		x, y := nodeID(a), nodeID(b)
		pair := x + "|" + y
		if y < x && a.Kind == b.Kind {
			pair = y + "|" + x
		}
		if e, ok := edgesByPair[pair]; ok {
			if e.label == "" && l.Label != "" {
				e.label = l.Label
				edgesByPair[pair] = e
			}
			continue
		}
		edgesByPair[pair] = edge{a: x, b: y, label: l.Label}
		if a.Kind == "router" && b.Kind == "device" {
			deviceRouters[y] = append(deviceRouters[y], pair)
		}
	}
	// A device seen by several routers keeps only the edges not on an uplink port.
	for _, pairs := range deviceRouters {
		if len(pairs) < 2 {
			continue
		}
		var access []string
		for _, p := range pairs {
			e := edgesByPair[p]
			if !uplink[e.a+"|"+e.label] {
				access = append(access, p)
			}
		}
		if len(access) == 0 {
			continue
		}
		for _, p := range pairs {
			e := edgesByPair[p]
			if uplink[e.a+"|"+e.label] {
				delete(edgesByPair, p)
			}
		}
	}

	var els []Element
	linked := map[string]bool{}
	for _, e := range edgesByPair {
		linked[e.a], linked[e.b] = true, true
	}
	extraNodes := map[string]Element{}
	for _, d := range devices {
		id := "device:" + d.Key
		if linked[id] {
			continue
		}
		var parent string
		if gw := gateways[d.Network]; gw != "" {
			parent = "router:" + gw
			routers[gw] = true
		} else {
			parent = "network:" + d.Network
			extraNodes[parent] = Element{Group: "nodes", Data: map[string]any{"id": parent, "kind": "network", "label": d.Network}}
		}
		edgesByPair[parent+"|"+id] = edge{a: parent, b: id}
	}

	for id := range routers {
		els = append(els, Element{Group: "nodes", Data: map[string]any{"id": "router:" + id, "kind": "router", "label": id}})
	}
	for _, n := range extraNodes {
		els = append(els, n)
	}
	for _, d := range devices {
		label := d.Label
		if label == "" {
			label = d.Hostname
		}
		ip := ""
		if d.IP.IsValid() {
			ip = d.IP.String()
		}
		if label == "" {
			label = ip
		}
		status := "online"
		if !d.Online {
			status = "offline"
		}
		els = append(els, Element{Group: "nodes", Data: map[string]any{
			"id": "device:" + d.Key, "key": d.Key, "kind": "device", "label": label, "status": status,
			"known": d.Known, "ip": ip, "mac": d.MAC, "vendor": d.Vendor, "hostname": d.Hostname,
			"network": d.Network, "port": d.Port, "lastSeen": d.LastSeen, "firstSeen": d.FirstSeen,
		}})
	}
	pairs := make([]string, 0, len(edgesByPair))
	for p := range edgesByPair {
		pairs = append(pairs, p)
	}
	sort.Strings(pairs)
	for _, p := range pairs {
		e := edgesByPair[p]
		els = append(els, Element{Group: "edges", Data: map[string]any{"id": "e:" + p, "source": e.a, "target": e.b, "label": e.label}})
	}
	sort.SliceStable(els, func(i, j int) bool {
		if els[i].Group != els[j].Group {
			return els[i].Group == "nodes"
		}
		return false
	})
	return els
}
