// Package source defines discovery sources and merges their observations.
package source

import (
	"context"
	"slices"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

// Source collects hosts and links for a network.
type Source interface {
	Name() string
	Collect(ctx context.Context, n config.Network) (model.Observations, error)
}

// Merge normalises MACs, drops hosts outside the network's CIDR or in its exclude list,
// collapses duplicates, attaches IP-only hosts to a MAC host with the same IP, and drops
// links pointing at filtered-out devices.
func Merge(n config.Network, obs model.Observations) model.Observations {
	out := obs
	out.Network = n.Name
	out.Hosts = nil
	index := map[string]int{} // device key → position in out.Hosts
	var ipOnly []model.Host
	dropped := map[string]bool{}

	for _, h := range obs.Hosts {
		h.MAC = model.NormalizeMAC(h.MAC)
		if !h.IP.IsValid() || !n.CIDR.Contains(h.IP) || slices.Contains(n.Exclude, h.IP) {
			dropped[model.DeviceKey(h)] = true
			continue
		}
		if h.MAC == "" {
			ipOnly = append(ipOnly, h)
			continue
		}
		key := model.DeviceKey(h)
		if i, ok := index[key]; ok {
			out.Hosts[i] = combine(out.Hosts[i], h)
			continue
		}
		index[key] = len(out.Hosts)
		out.Hosts = append(out.Hosts, h)
	}
	for _, h := range ipOnly {
		attached := false
		for i := range out.Hosts {
			if out.Hosts[i].IP == h.IP {
				mac := out.Hosts[i].MAC
				out.Hosts[i] = combine(out.Hosts[i], h)
				out.Hosts[i].MAC = mac
				attached = true
				break
			}
		}
		if attached {
			continue
		}
		key := model.DeviceKey(h)
		if i, ok := index[key]; ok {
			out.Hosts[i] = combine(out.Hosts[i], h)
			continue
		}
		index[key] = len(out.Hosts)
		out.Hosts = append(out.Hosts, h)
	}

	out.Links = nil
	for _, l := range obs.Links {
		if (l.A.Kind == "device" && dropped[l.A.ID]) || (l.B.Kind == "device" && dropped[l.B.ID]) {
			continue
		}
		out.Links = append(out.Links, l)
	}
	return out
}

// combine fills empty fields of a from b; a non-empty Port from b wins (later sources
// such as the router know the port better than nmap).
func combine(a, b model.Host) model.Host {
	if a.MAC == "" {
		a.MAC = b.MAC
	}
	if a.Hostname == "" {
		a.Hostname = b.Hostname
	}
	if a.Vendor == "" {
		a.Vendor = b.Vendor
	}
	if b.Port != "" {
		a.Port = b.Port
	}
	if a.Source == "" {
		a.Source = b.Source
	}
	return a
}
