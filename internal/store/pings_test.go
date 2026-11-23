package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

const ms = time.Millisecond

func pingStore(t *testing.T, keys ...string) *Store {
	t.Helper()
	s, _ := openTemp(t)
	for _, k := range keys {
		d := sampleDevice()
		d.Network, d.Key = "lan", k
		if err := s.UpsertDevices(context.Background(), []model.Device{d}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

var pingT0 = time.Unix(1_700_000_000, 0).UTC()

func record(t *testing.T, s *Store, origin, key string, rtts ...time.Duration) {
	t.Helper()
	for i, r := range rtts {
		if err := s.RecordPings(context.Background(), "lan", origin, pingT0.Add(time.Duration(i)*time.Minute),
			[]model.PingSample{{Key: key, RTT: r}}); err != nil {
			t.Fatal(err)
		}
	}
}

func ping(t *testing.T, s *Store, network, key string) model.Ping {
	t.Helper()
	m, err := s.Pings(context.Background(), network)
	if err != nil {
		t.Fatal(err)
	}
	return m[model.MemberRef{Network: network, Key: key}]
}

func TestPingsMedianOfLastFive(t *testing.T) {
	s := pingStore(t, "a")
	record(t, s, "gw", "a", 1*ms, 2*ms, 3*ms, 4*ms, 100*ms, 5*ms)
	p := ping(t, s, "lan", "a")
	if !p.OK || p.RTT != 4*ms || p.Origin != "gw" || !p.At.Equal(pingT0.Add(5*time.Minute)) {
		t.Fatalf("ping = %+v", p)
	}
}

func TestPingsThreeMissesNoValue(t *testing.T) {
	s := pingStore(t, "a")
	record(t, s, "gw", "a", 5*ms, -1, -1)
	if p := ping(t, s, "lan", "a"); !p.OK || p.RTT != 5*ms || !p.At.Equal(pingT0) {
		t.Fatalf("after two misses: %+v", p)
	}
	record(t, s, "gw", "a", -1)
	if p := ping(t, s, "lan", "a"); p.OK {
		t.Fatalf("after three misses: %+v", p)
	}
}

func TestPingsOriginChangeResets(t *testing.T) {
	s := pingStore(t, "a")
	record(t, s, "netmon", "a", 100*ms, 100*ms)
	record(t, s, "gw", "a", 2*ms)
	if p := ping(t, s, "lan", "a"); p.RTT != 2*ms || p.Origin != "gw" {
		t.Fatalf("ping = %+v", p)
	}
}

func TestPingsUnknownKeyIgnored(t *testing.T) {
	s := pingStore(t, "a")
	record(t, s, "gw", "gone", 2*ms)
	if m, _ := s.Pings(context.Background(), ""); len(m) != 0 {
		t.Fatalf("pings = %v", m)
	}
}

func TestPingsFollowDevice(t *testing.T) {
	ctx := context.Background()
	s := pingStore(t, "a", "b")
	record(t, s, "gw", "a", 7*ms)
	d := sampleDevice()
	d.Network, d.Key, d.PrevKey = "lan", "c", "a"
	if err := s.UpsertDevices(ctx, []model.Device{d}); err != nil {
		t.Fatal(err)
	}
	if p := ping(t, s, "lan", "c"); p.RTT != 7*ms {
		t.Fatalf("after rename: %+v", p)
	}
	record(t, s, "gw", "b", 1*ms, 9*ms) // answered later than c
	d = sampleDevice()
	d.Network, d.Key, d.MergedInto = "lan", "b", "c"
	if err := s.UpsertDevices(ctx, []model.Device{d}); err != nil {
		t.Fatal(err)
	}
	if p := ping(t, s, "lan", "c"); p.RTT != 5*ms {
		t.Fatalf("after merge: %+v", p)
	}
	if err := s.RenameNetwork(ctx, "lan", "home"); err != nil {
		t.Fatal(err)
	}
	if p := ping(t, s, "home", "c"); p.RTT != 5*ms {
		t.Fatalf("after network rename: %+v", p)
	}
	if err := s.DeleteDevice(ctx, "home", "c"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Pings(ctx, ""); len(m) != 0 {
		t.Fatalf("after delete: %v", m)
	}
	s2 := pingStore(t, "a")
	record(t, s2, "gw", "a", ms)
	if err := s2.DeleteNetworkData(ctx, "lan"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s2.Pings(ctx, ""); len(m) != 0 {
		t.Fatalf("after network delete: %v", m)
	}
}

func hist(t *testing.T, s *Store, network, key string) []model.PingPoint {
	t.Helper()
	h, err := s.PingHistory(context.Background(), network, key, pingT0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPingHistoryKeepsEveryRun(t *testing.T) {
	s := pingStore(t, "a", "b")
	record(t, s, "gw", "a", 1*ms, 2*ms, 3*ms, 4*ms, -1, 6*ms, 7*ms)
	record(t, s, "gw", "b", 9*ms)
	record(t, s, "gw", "gone", 9*ms)
	h := hist(t, s, "lan", "a")
	if len(h) != 7 || h[0].RTT != ms || !h[0].At.Equal(pingT0) || h[4].OK || !h[6].OK || h[6].Origin != "gw" {
		t.Fatalf("history = %+v", h)
	}
	h, _ = s.PingHistory(context.Background(), "lan", "a", pingT0.Add(5*time.Minute))
	if len(h) != 2 {
		t.Fatalf("since: %+v", h)
	}
	if h := hist(t, s, "lan", "gone"); len(h) != 0 {
		t.Fatalf("unknown device recorded: %+v", h)
	}
}

func TestPingHistoryPages(t *testing.T) {
	ctx := context.Background()
	s := pingStore(t, "a")
	// Two runs share a second: paging must not lose or repeat one.
	for i := range 7 {
		s.RecordPings(ctx, "lan", "gw", pingT0.Add(time.Duration(i/2)*time.Second), []model.PingSample{{Key: "a", RTT: time.Duration(i) * ms}})
	}
	var got []time.Duration
	cur := PingCursor{}
	for range 5 {
		rows, next, err := s.PingPage(ctx, "lan", "a", cur, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			got = append(got, r.RTT/ms)
		}
		if next == (PingCursor{}) {
			break
		}
		cur = next
	}
	want := []time.Duration{6, 5, 4, 3, 2, 1, 0}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestPingHistoryFollowsDeviceAndPrunes(t *testing.T) {
	ctx := context.Background()
	s := pingStore(t, "a", "b")
	record(t, s, "gw", "a", 1*ms, 2*ms)
	record(t, s, "gw", "b", 3*ms)
	d := sampleDevice()
	d.Network, d.Key, d.PrevKey = "lan", "c", "a"
	s.UpsertDevices(ctx, []model.Device{d})
	d = sampleDevice()
	d.Network, d.Key, d.MergedInto = "lan", "b", "c"
	s.UpsertDevices(ctx, []model.Device{d})
	if h := hist(t, s, "lan", "c"); len(h) != 3 {
		t.Fatalf("after rename and merge: %+v", h)
	}
	s.RenameNetwork(ctx, "lan", "home")
	if h := hist(t, s, "home", "c"); len(h) != 3 {
		t.Fatalf("after network rename: %+v", h)
	}
	if err := s.PrunePings(ctx, pingT0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if h := hist(t, s, "home", "c"); len(h) != 1 {
		t.Fatalf("after prune: %+v", h)
	}
	s.DeleteDevice(ctx, "home", "c")
	if h := hist(t, s, "home", "c"); len(h) != 0 {
		t.Fatalf("after delete: %+v", h)
	}
	s2 := pingStore(t, "a")
	record(t, s2, "gw", "a", ms)
	s2.DeleteNetworkData(ctx, "lan")
	if h := hist(t, s2, "lan", "a"); len(h) != 0 {
		t.Fatalf("after network delete: %+v", h)
	}
}
