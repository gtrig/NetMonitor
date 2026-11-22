package diff

import (
	"net/netip"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

var t0 = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func host(mac, addr, name string) model.Host {
	return model.Host{MAC: mac, IP: ip(addr), Hostname: name, Source: "nmap"}
}

func dev(mac, addr, name string) model.Device {
	h := model.Host{MAC: mac, IP: ip(addr)}
	return model.Device{Key: model.DeviceKey(h), Network: "lan", MAC: mac, IP: ip(addr), Hostname: name,
		Online: true, FirstSeen: t0.Add(-time.Hour), LastSeen: t0.Add(-time.Minute), Sources: []string{"nmap"}}
}

func obs(hosts ...model.Host) model.Observations {
	return model.Observations{Network: "lan", Source: "router:core", At: t0, Hosts: hosts}
}

func types(evs []model.Event) map[string]int {
	m := map[string]int{}
	for _, e := range evs {
		m[e.Type]++
	}
	return m
}

func TestFirstCollectionEmitsBaseline(t *testing.T) {
	changed, evs := Apply(nil, obs(
		host("aa:00:00:00:00:01", "10.0.0.1", "a"),
		host("aa:00:00:00:00:02", "10.0.0.2", "b"),
		host("", "10.0.0.3", "c"),
	), true)
	if len(changed) != 3 {
		t.Fatalf("changed = %d", len(changed))
	}
	if len(evs) != 1 || evs[0].Type != model.EventBaseline || evs[0].Message != "Baseline: 3 devices discovered" {
		t.Fatalf("events = %+v", evs)
	}
	for _, d := range changed {
		if !d.Online || !d.FirstSeen.Equal(t0) || !d.LastSeen.Equal(t0) {
			t.Fatalf("device %+v", d)
		}
	}
}

func TestNewDevice(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.1", "a")}
	_, evs := Apply(known, obs(host("aa:00:00:00:00:01", "10.0.0.1", "a"), host("aa:00:00:00:00:02", "10.0.0.2", "b")), false)
	if len(evs) != 1 || evs[0].Type != model.EventDeviceNew || evs[0].DeviceKey != "mac:aa:00:00:00:00:02" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestIPChanged(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.1", "a")}
	changed, evs := Apply(known, obs(host("aa:00:00:00:00:01", "10.0.0.9", "a")), false)
	if len(evs) != 1 || evs[0].Type != model.EventIPChanged || evs[0].Old != "10.0.0.1" || evs[0].New != "10.0.0.9" {
		t.Fatalf("events = %+v", evs)
	}
	if changed[0].IP != ip("10.0.0.9") {
		t.Fatalf("ip not updated")
	}
}

func TestHostnameChanged(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.1", "old")}
	_, evs := Apply(known, obs(host("aa:00:00:00:00:01", "10.0.0.1", "new")), false)
	if len(evs) != 1 || evs[0].Type != model.EventHostnameChanged || evs[0].Old != "old" || evs[0].New != "new" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestEmptyHostnameKeepsOld(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.1", "nas")}
	changed, evs := Apply(known, obs(host("aa:00:00:00:00:01", "10.0.0.1", "")), false)
	if len(evs) != 0 || changed[0].Hostname != "nas" {
		t.Fatalf("events=%+v hostname=%q", evs, changed[0].Hostname)
	}
}

func TestBackOnline(t *testing.T) {
	d := dev("aa:00:00:00:00:01", "10.0.0.1", "a")
	d.Online = false
	changed, evs := Apply([]model.Device{d}, obs(host("aa:00:00:00:00:01", "10.0.0.1", "a")), false)
	if len(evs) != 1 || evs[0].Type != model.EventDeviceBackOnline || !changed[0].Online {
		t.Fatalf("events = %+v", evs)
	}
}

func TestSourcesUnion(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.1", "a")}
	changed, _ := Apply(known, obs(host("aa:00:00:00:00:01", "10.0.0.1", "a")), false)
	if len(changed[0].Sources) != 2 {
		t.Fatalf("sources = %v", changed[0].Sources)
	}
}

func TestMACChangedForIP(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.5", "a")}
	_, evs := Apply(known, obs(host("bb:00:00:00:00:01", "10.0.0.5", "")), false)
	got := types(evs)
	if got[model.EventMACChangedForIP] != 1 || got[model.EventDeviceNew] != 1 {
		t.Fatalf("events = %+v", evs)
	}
}

func TestApplyIPOnlyMatchesKnownMACDevice(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.5", "a")}
	changed, evs := Apply(known, obs(host("", "10.0.0.5", "")), false)
	if len(evs) != 0 || len(changed) != 1 || changed[0].Key != "mac:aa:00:00:00:00:01" || !changed[0].LastSeen.Equal(t0) {
		t.Fatalf("changed=%+v events=%+v", changed, evs)
	}
}

func TestOfflineAfterThreshold(t *testing.T) {
	d := dev("aa:00:00:00:00:01", "10.0.0.1", "a")
	d.LastSeen = t0.Add(-20 * time.Minute)
	last := map[string]time.Time{"nmap": t0}
	after := map[string]time.Duration{"nmap": 15 * time.Minute}
	changed, evs := Offline([]model.Device{d}, "lan", last, after)
	if len(evs) != 1 || evs[0].Type != model.EventDeviceOffline || changed[0].Online {
		t.Fatalf("events = %+v", evs)
	}
	d.LastSeen = t0.Add(-10 * time.Minute)
	if _, evs := Offline([]model.Device{d}, "lan", last, after); len(evs) != 0 {
		t.Fatalf("unexpected %+v", evs)
	}
}

func TestOfflineIgnoresFailedCollections(t *testing.T) {
	// The last successful collection saw the device; nothing succeeded since.
	d := dev("aa:00:00:00:00:01", "10.0.0.1", "a")
	d.LastSeen = t0
	if _, evs := Offline([]model.Device{d}, "lan", map[string]time.Time{"nmap": t0},
		map[string]time.Duration{"nmap": 15 * time.Minute}); len(evs) != 0 {
		t.Fatalf("unexpected %+v", evs)
	}
}

func TestOfflineWaitsForEverySourceThatSawDevice(t *testing.T) {
	// Seen only by nmap (15m interval). The router polls every minute but never sees it,
	// so the router's fresh success must not mark it offline.
	d := dev("", "10.0.0.9", "")
	d.Sources = []string{"nmap"}
	d.LastSeen = t0.Add(-10 * time.Minute)
	last := map[string]time.Time{"router:core": t0, "nmap": t0.Add(-10 * time.Minute)}
	after := map[string]time.Duration{"router:core": 3 * time.Minute, "nmap": 45 * time.Minute}
	if _, evs := Offline([]model.Device{d}, "lan", last, after); len(evs) != 0 {
		t.Fatalf("unexpected %+v", evs)
	}
	// Seen by both: offline only when both have missed it long enough.
	d.Sources = []string{"nmap", "router:core"}
	d.LastSeen = t0.Add(-50 * time.Minute)
	last["nmap"] = t0
	if _, evs := Offline([]model.Device{d}, "lan", last, after); len(evs) != 1 {
		t.Fatalf("expected offline, got %+v", evs)
	}
}

func TestOfflineSkipsAlreadyOfflineAndOtherNetworks(t *testing.T) {
	a := dev("aa:00:00:00:00:01", "10.0.0.1", "a")
	a.Online = false
	a.LastSeen = t0.Add(-time.Hour)
	b := dev("aa:00:00:00:00:02", "10.0.0.2", "b")
	b.Network = "iot"
	b.LastSeen = t0.Add(-time.Hour)
	if _, evs := Offline([]model.Device{a, b}, "lan", map[string]time.Time{"nmap": t0},
		map[string]time.Duration{"nmap": 15 * time.Minute}); len(evs) != 0 {
		t.Fatalf("unexpected %+v", evs)
	}
}

func TestIPOnlyDeviceUpgradedWhenMACAppears(t *testing.T) {
	old := dev("", "10.0.0.5", "nas")
	old.Label = "My NAS"
	changed, evs := Apply([]model.Device{old}, obs(host("aa:00:00:00:00:05", "10.0.0.5", "")), false)
	if len(evs) != 0 {
		t.Fatalf("events = %+v", evs)
	}
	if len(changed) != 1 || changed[0].Key != "mac:aa:00:00:00:00:05" || changed[0].PrevKey != "ip:10.0.0.5" ||
		changed[0].MAC != "aa:00:00:00:00:05" || changed[0].Hostname != "nas" || !changed[0].FirstSeen.Equal(old.FirstSeen) {
		t.Fatalf("changed = %+v", changed)
	}
}

// Finding 2: DNS-derived names never overwrite a known name; same-host names don't emit.
func TestDNSNameDoesNotOverwriteKnownHostname(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.1", "phone")}
	h := host("aa:00:00:00:00:01", "10.0.0.1", "phone.lan")
	h.NameFromDNS = true
	changed, evs := Apply(known, obs(h), false)
	if len(evs) != 0 || changed[0].Hostname != "phone" {
		t.Fatalf("events=%+v hostname=%q", evs, changed[0].Hostname)
	}
}

func TestLeaseNameReplacesDNSNameSilently(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.1", "phone.lan")}
	changed, evs := Apply(known, obs(host("aa:00:00:00:00:01", "10.0.0.1", "phone")), false)
	if len(evs) != 0 || changed[0].Hostname != "phone" {
		t.Fatalf("events=%+v hostname=%q", evs, changed[0].Hostname)
	}
}

// Finding 3: per-source last-seen; a fast source missing 3 times + slow source's latest
// collection not seeing it → offline, without waiting for 3 slow misses.
func TestOfflineUsesPerSourceSeen(t *testing.T) {
	d := dev("aa:00:00:00:00:01", "10.0.0.1", "a")
	d.Sources = []string{"router:core", "nmap"}
	d.SeenBy = map[string]time.Time{"router:core": t0.Add(-5 * time.Minute), "nmap": t0.Add(-10 * time.Minute)}
	d.LastSeen = t0.Add(-5 * time.Minute)
	after := map[string]time.Duration{"router:core": 3 * time.Minute, "nmap": 45 * time.Minute}
	// nmap's latest collection (t0-10m) saw it: still online.
	last := map[string]time.Time{"router:core": t0, "nmap": t0.Add(-10 * time.Minute)}
	if _, evs := Offline([]model.Device{d}, "lan", last, after); len(evs) != 0 {
		t.Fatalf("unexpected %+v", evs)
	}
	// nmap ran again at t0 and missed it: offline now.
	last["nmap"] = t0
	if _, evs := Offline([]model.Device{d}, "lan", last, after); len(evs) != 1 {
		t.Fatal("expected offline")
	}
}

func TestApplyRecordsSeenBy(t *testing.T) {
	known := []model.Device{dev("aa:00:00:00:00:01", "10.0.0.1", "a")}
	changed, _ := Apply(known, obs(host("aa:00:00:00:00:01", "10.0.0.1", "a")), false)
	if !changed[0].SeenBy["router:core"].Equal(t0) {
		t.Fatalf("seenBy = %v", changed[0].SeenBy)
	}
}

// Finding 8: an IP-only row at an IP taken over by an existing MAC device is folded into it.
func TestGhostIPOnlyDeviceMergedIntoMACDevice(t *testing.T) {
	macDev := dev("aa:00:00:00:00:01", "10.0.0.9", "a")
	ghost := dev("", "10.0.0.5", "")
	changed, evs := Apply([]model.Device{macDev, ghost}, obs(host("aa:00:00:00:00:01", "10.0.0.5", "")), false)
	var merged bool
	for _, c := range changed {
		if c.Key == "ip:10.0.0.5" && c.MergedInto == "mac:aa:00:00:00:00:01" {
			merged = true
		}
	}
	if !merged || types(evs)[model.EventIPChanged] != 1 {
		t.Fatalf("changed=%+v events=%+v", changed, evs)
	}
}
