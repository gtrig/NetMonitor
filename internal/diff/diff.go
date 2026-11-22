// Package diff compares observations with known devices and produces change events.
package diff

import (
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// Describe returns a short human-readable name for a device.
func Describe(d model.Device) string {
	name := d.Label
	if name == "" {
		name = d.Hostname
	}
	addr := ""
	if d.IP.IsValid() {
		addr = d.IP.String()
	}
	switch {
	case name != "" && addr != "":
		name = fmt.Sprintf("%s (%s)", name, addr)
	case name == "":
		name = addr
	}
	if d.MAC != "" {
		name += " " + d.MAC
	}
	if d.Vendor != "" {
		name += " [" + d.Vendor + "]"
	}
	return name
}

// Apply merges one network's observations into the known devices of that network.
// When firstEver is true a single baseline event replaces the per-device device_new events.
func Apply(known []model.Device, obs model.Observations, firstEver bool) ([]model.Device, []model.Event) {
	at := obs.At
	byKey := map[string]int{}
	byIP := map[netip.Addr][]int{}
	devs := slices.Clone(known)
	for i, d := range devs {
		byKey[d.Key] = i
		if d.IP.IsValid() {
			byIP[d.IP] = append(byIP[d.IP], i)
		}
	}
	touched := map[int]bool{}
	var events []model.Event
	var newCount int
	ev := func(typ string, d model.Device, msg, old, nw string) {
		events = append(events, model.Event{Type: typ, Network: obs.Network, DeviceKey: d.Key, Message: msg, At: at, Old: old, New: nw})
	}

	for _, h := range obs.Hosts {
		h.MAC = model.NormalizeMAC(h.MAC)
		key := model.DeviceKey(h)
		idx, ok := byKey[key]
		if !ok && h.MAC == "" && h.IP.IsValid() {
			// IP-only host: attach to a known device holding that IP, preferring MAC devices.
			for _, i := range byIP[h.IP] {
				if !ok || devs[i].MAC != "" {
					idx, ok = i, true
				}
			}
		}
		if !ok && h.MAC != "" && h.IP.IsValid() {
			// An IP-only device at this IP now has a known MAC: upgrade its identity.
			for _, i := range byIP[h.IP] {
				if devs[i].MAC == "" {
					idx, ok = i, true
					devs[i].PrevKey = devs[i].Key
					devs[i].Key = key
					devs[i].MAC = h.MAC
					byKey[key] = i
					break
				}
			}
		}
		if !ok {
			d := model.Device{Key: key, Network: obs.Network, MAC: h.MAC, IP: h.IP, Hostname: h.Hostname,
				Vendor: h.Vendor, Port: h.Port, Online: true, FirstSeen: at, LastSeen: at, Sources: []string{sourceOf(h, obs)}}
			// Possible spoof / replacement: another MAC device held this IP.
			if h.MAC != "" {
				for _, i := range byIP[h.IP] {
					if devs[i].MAC != "" && devs[i].MAC != h.MAC && devs[i].IP == h.IP {
						ev(model.EventMACChangedForIP, d,
							fmt.Sprintf("IP %s moved from %s to %s", h.IP, devs[i].MAC, h.MAC), devs[i].MAC, h.MAC)
					}
				}
			}
			devs = append(devs, d)
			i := len(devs) - 1
			byKey[key] = i
			byIP[h.IP] = append(byIP[h.IP], i)
			touched[i] = true
			newCount++
			if !firstEver {
				ev(model.EventDeviceNew, d, "New device: "+Describe(d), "", "")
			}
			continue
		}

		d := &devs[idx]
		touched[idx] = true
		if h.IP.IsValid() && d.IP.IsValid() && h.IP != d.IP && h.MAC != "" {
			ev(model.EventIPChanged, *d, fmt.Sprintf("%s changed IP %s → %s", Describe(*d), d.IP, h.IP), d.IP.String(), h.IP.String())
			d.IP = h.IP
		} else if !d.IP.IsValid() {
			d.IP = h.IP
		}
		if h.Hostname != "" && h.Hostname != d.Hostname {
			if d.Hostname != "" {
				ev(model.EventHostnameChanged, *d, fmt.Sprintf("%s hostname %q → %q", Describe(*d), d.Hostname, h.Hostname), d.Hostname, h.Hostname)
			}
			d.Hostname = h.Hostname
		}
		if h.Vendor != "" {
			d.Vendor = h.Vendor
		}
		if h.Port != "" {
			d.Port = h.Port
		}
		if d.MAC == "" && h.MAC != "" {
			d.MAC = h.MAC
		}
		if !d.Online {
			d.Online = true
			ev(model.EventDeviceBackOnline, *d, "Back online: "+Describe(*d), "", "")
		}
		d.LastSeen = at
		if s := sourceOf(h, obs); !slices.Contains(d.Sources, s) {
			d.Sources = append(d.Sources, s)
		}
	}

	if firstEver && newCount > 0 {
		events = append(events, model.Event{Type: model.EventBaseline, Network: obs.Network, At: at,
			Message: fmt.Sprintf("Baseline: %d devices discovered", newCount)})
	}

	var changed []model.Device
	for i := range devs {
		if touched[i] {
			changed = append(changed, devs[i])
		}
	}
	return changed, events
}

func sourceOf(h model.Host, obs model.Observations) string {
	if h.Source != "" && obs.Source == "" {
		return h.Source
	}
	return obs.Source
}

// Offline marks online devices of network as offline once every source that has seen the
// device has completed a successful collection more than that source's offline threshold after
// the device was last seen. lastSuccess and after are keyed by source name. It never reads the
// clock, so failed collections can't cause false offline events.
func Offline(known []model.Device, network string, lastSuccess map[string]time.Time, after map[string]time.Duration) ([]model.Device, []model.Event) {
	var changed []model.Device
	var events []model.Event
	for _, d := range known {
		if d.Network != network || !d.Online {
			continue
		}
		missedByAll, applicable := true, false
		var at time.Time
		for _, src := range d.Sources {
			ls, ok := lastSuccess[src]
			th, ok2 := after[src]
			if !ok || !ok2 {
				continue // source no longer configured
			}
			applicable = true
			if ls.Sub(d.LastSeen) <= th {
				missedByAll = false
				break
			}
			if ls.After(at) {
				at = ls
			}
		}
		if !applicable || !missedByAll {
			continue
		}
		d.Online = false
		changed = append(changed, d)
		events = append(events, model.Event{Type: model.EventDeviceOffline, Network: network, DeviceKey: d.Key,
			At: at, Message: "Offline: " + Describe(d)})
	}
	return changed, events
}
