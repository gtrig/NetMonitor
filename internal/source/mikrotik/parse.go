package mikrotik

import (
	"net/netip"
	"strings"

	"github.com/gtrig/netmon/internal/model"
)

// parseARP turns /ip/arp rows into hosts, skipping incomplete, failed, invalid and disabled entries.
func parseARP(rows []map[string]string) []model.Host {
	var out []model.Host
	for _, r := range rows {
		switch r["status"] {
		case "incomplete", "failed", "permanent":
			continue // static entries don't prove the device is present
		}
		if r["dhcp"] == "true" || r["dynamic"] == "false" {
			continue // DHCP add-arp / static entries live for the whole lease
		}
		if r["invalid"] == "true" || r["disabled"] == "true" || r["complete"] == "false" {
			continue
		}
		mac := model.NormalizeMAC(r["mac-address"])
		ip, err := netip.ParseAddr(r["address"])
		if mac == "" || err != nil {
			continue
		}
		out = append(out, model.Host{IP: ip, MAC: mac, Port: r["interface"]})
	}
	return out
}

// parseLeases returns MAC → host-name. Leases are used for names only, never presence.
func parseLeases(rows []map[string]string) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		if mac := model.NormalizeMAC(r["mac-address"]); mac != "" && r["host-name"] != "" {
			out[mac] = r["host-name"]
		}
	}
	return out
}

// parseBridgeHosts returns MAC → on-interface for non-local bridge host entries.
func parseBridgeHosts(rows []map[string]string) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		if r["local"] == "true" {
			continue
		}
		if mac := model.NormalizeMAC(r["mac-address"]); mac != "" && r["on-interface"] != "" {
			out[mac] = r["on-interface"]
		}
	}
	return out
}

// Resolver maps a router (by management address and identity) to its node ID,
// e.g. the configured router name. It returns identity when it knows no better.
type Resolver func(address, identity string) string

// parseNeighbors returns router↔router links for discovered neighbors (MNDP/LLDP/CDP).
// local is this router's node ID.
func parseNeighbors(local string, rows []map[string]string, resolve Resolver) []model.Link {
	var out []model.Link
	seen := map[string]bool{}
	for _, r := range rows {
		if r["identity"] == "" {
			continue
		}
		addr, _, _ := strings.Cut(r["address"], ",")
		id := resolve(addr, r["identity"])
		if id == "" || id == local || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, model.Link{
			A:     model.NodeRef{Kind: "router", ID: local},
			B:     model.NodeRef{Kind: "router", ID: id},
			Label: r["interface"],
		})
	}
	return out
}
