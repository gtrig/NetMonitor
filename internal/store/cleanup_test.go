package store

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

func dev(network, key, ip string) model.Device {
	now := time.Now().Truncate(time.Second).UTC()
	d := model.Device{Key: key, Network: network, Online: true, FirstSeen: now, LastSeen: now, Sources: []string{"nmap"}}
	d.IP, _ = netip.ParseAddr(ip)
	return d
}

func mustUpsert(t *testing.T, s *Store, ds ...model.Device) {
	t.Helper()
	if err := s.UpsertDevices(context.Background(), ds); err != nil {
		t.Fatal(err)
	}
}

func countDevices(t *testing.T, s *Store, network string) int {
	t.Helper()
	ds, err := s.Devices(context.Background(), network)
	if err != nil {
		t.Fatal(err)
	}
	return len(ds)
}

func TestRenameNetworkIntoExisting(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	mustUpsert(t, s, dev("lan", "mac:aa", "10.0.0.2"), dev("lan", "ip:10.0.0.9", "10.0.0.9"), dev("GTLan", "mac:aa", "10.0.0.2"))
	// label only on the lan row
	s.db.Exec(`UPDATE devices SET label = 'nas', known = 1 WHERE network = 'lan' AND key = 'mac:aa'`)
	old := time.Unix(1000, 0).UTC()
	newer := time.Unix(2000, 0).UTC()
	s.SetLastSuccess(ctx, "lan", "nmap", old)
	s.SetLastSuccess(ctx, "GTLan", "nmap", newer)
	s.AddEvents(ctx, []model.Event{{Type: model.EventDeviceNew, Network: "lan", At: old}})
	s.ReplaceLinks(ctx, "lan", "nmap", []model.Link{{A: model.NodeRef{Kind: "router", ID: "a"}, B: model.NodeRef{Kind: "router", ID: "b"}}})

	if err := s.RenameNetwork(ctx, "lan", "GTLan"); err != nil {
		t.Fatal(err)
	}
	if n := countDevices(t, s, "lan"); n != 0 {
		t.Fatalf("lan devices = %d", n)
	}
	ds, _ := s.Devices(ctx, "GTLan")
	if len(ds) != 2 {
		t.Fatalf("GTLan devices = %d, want 2", len(ds))
	}
	for _, d := range ds {
		if d.Key == "mac:aa" && (d.Label != "nas" || !d.Known) {
			t.Fatalf("meta not carried: %+v", d)
		}
	}
	evs, _ := s.Events(ctx, EventFilter{Network: "GTLan", Limit: 10})
	if len(evs) != 1 {
		t.Fatalf("events renamed = %d", len(evs))
	}
	last, _ := s.LastSuccesses(ctx, "GTLan")
	if !last["nmap"].Equal(newer) {
		t.Fatalf("network_state = %v, want existing GTLan time", last)
	}
	if l, _ := s.LastSuccesses(ctx, "lan"); len(l) != 0 {
		t.Fatalf("lan state left: %v", l)
	}
	if links, _ := s.Links(ctx, "lan"); len(links) != 0 {
		t.Fatalf("lan links left: %v", links)
	}
}

func TestRenameNetworkToNew(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	mustUpsert(t, s, dev("lan", "mac:aa", "10.0.0.2"))
	s.SetLastSuccess(ctx, "lan", "nmap", time.Unix(1000, 0))
	if err := s.RenameNetwork(ctx, "lan", "home"); err != nil {
		t.Fatal(err)
	}
	if countDevices(t, s, "home") != 1 {
		t.Fatal("device not moved")
	}
	if l, _ := s.LastSuccesses(ctx, "home"); len(l) != 1 {
		t.Fatalf("state not moved: %v", l)
	}
}

func TestDataNetworksAndDelete(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	mustUpsert(t, s, dev("lan", "mac:aa", "10.0.0.2"), dev("lan", "mac:bb", "10.0.0.3"), dev("GTLan", "mac:aa", "10.0.0.2"))
	s.AddEvents(ctx, []model.Event{{Type: model.EventDeviceNew, Network: "lan", At: time.Now()}})
	nd, err := s.DataNetworks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]NetworkData{}
	for _, n := range nd {
		got[n.Name] = n
	}
	if got["lan"].Devices != 2 || got["lan"].Events != 1 || got["GTLan"].Devices != 1 {
		t.Fatalf("DataNetworks = %+v", nd)
	}
	if err := s.DeleteNetworkData(ctx, "lan"); err != nil {
		t.Fatal(err)
	}
	if countDevices(t, s, "lan") != 0 {
		t.Fatal("devices not deleted")
	}
	nd, _ = s.DataNetworks(ctx)
	for _, n := range nd {
		if n.Name == "lan" && (n.Devices != 0 || n.Events != 1) {
			t.Fatalf("after delete lan = %+v", n)
		}
	}
}

func TestDeleteDevice(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	mustUpsert(t, s, dev("lan", "mac:aa", "10.0.0.2"), dev("lan", "mac:bb", "10.0.0.3"))
	s.MergeMachine(ctx, []model.MemberRef{{Network: "lan", Key: "mac:aa"}, {Network: "lan", Key: "mac:bb"}})
	if err := s.DeleteDevice(ctx, "lan", "mac:aa"); err != nil {
		t.Fatal(err)
	}
	if countDevices(t, s, "lan") != 1 {
		t.Fatal("not deleted")
	}
	if m, _ := s.Machines(ctx); len(m) != 0 {
		t.Fatalf("machine with one member left: %v", m)
	}
	if err := s.DeleteDevice(ctx, "lan", "mac:aa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestMergeAndSplitMachine(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a := model.MemberRef{Network: "GTLan", Key: "mac:aa"}
	b := model.MemberRef{Network: "VPN", Key: "ip:10.0.10.5"}
	c := model.MemberRef{Network: "ATLan", Key: "ip:192.168.88.1"}
	d := model.MemberRef{Network: "VPN", Key: "ip:10.0.10.6"}
	mustUpsert(t, s, dev("GTLan", "mac:aa", "192.168.8.1"), dev("VPN", "ip:10.0.10.5", "10.0.10.5"),
		dev("ATLan", "ip:192.168.88.1", "192.168.88.1"), dev("VPN", "ip:10.0.10.6", "10.0.10.6"))

	if _, err := s.MergeMachine(ctx, []model.MemberRef{a}); err == nil {
		t.Fatal("merge of one member should fail")
	}
	if _, err := s.MergeMachine(ctx, []model.MemberRef{a, {Network: "x", Key: "nope"}}); err == nil {
		t.Fatal("merge with unknown device should fail")
	}
	id1, err := s.MergeMachine(ctx, []model.MemberRef{a, b})
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s.MergeMachine(ctx, []model.MemberRef{c, d})
	if id1 == id2 {
		t.Fatal("separate machines share an id")
	}
	// Joining a member of each machine folds both into the smaller id.
	id3, err := s.MergeMachine(ctx, []model.MemberRef{d, b})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.Machines(ctx)
	if id3 != min(id1, id2) || len(m) != 4 || m[a] != id3 || m[c] != id3 {
		t.Fatalf("after fold: id=%d machines=%v", id3, m)
	}
	for _, r := range []model.MemberRef{a, b, c} {
		if err := s.SplitMember(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if m, _ := s.Machines(ctx); len(m) != 0 {
		t.Fatalf("single-member machine left: %v", m)
	}
}

func TestMembershipFollowsPrevKeyAndMergedInto(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	mustUpsert(t, s, dev("lan", "ip:10.0.0.2", "10.0.0.2"), dev("vpn", "ip:10.0.10.2", "10.0.10.2"), dev("lan", "ip:10.0.0.3", "10.0.0.3"))
	id, _ := s.MergeMachine(ctx, []model.MemberRef{{Network: "lan", Key: "ip:10.0.0.2"}, {Network: "vpn", Key: "ip:10.0.10.2"}})

	// IP-only device gains a MAC.
	d := dev("lan", "mac:aa", "10.0.0.2")
	d.PrevKey = "ip:10.0.0.2"
	mustUpsert(t, s, d)
	m, _ := s.Machines(ctx)
	if m[model.MemberRef{Network: "lan", Key: "mac:aa"}] != id || len(m) != 2 {
		t.Fatalf("after PrevKey: %v", m)
	}

	// vpn member is folded into another (new) device: membership moves to the target.
	mustUpsert(t, s, dev("vpn", "mac:bb", "10.0.10.2"))
	g := dev("vpn", "ip:10.0.10.2", "10.0.10.2")
	g.MergedInto = "mac:bb"
	mustUpsert(t, s, g)
	m, _ = s.Machines(ctx)
	if m[model.MemberRef{Network: "vpn", Key: "mac:bb"}] != id || len(m) != 2 {
		t.Fatalf("after MergedInto: %v", m)
	}
}
