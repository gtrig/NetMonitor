package scheduler

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

type fakePinger struct {
	asked  []netip.Addr
	answer map[netip.Addr]time.Duration
	err    error
	during func()
}

func (f *fakePinger) Name() string   { return "ping:gw" }
func (f *fakePinger) Origin() string { return "gw" }
func (f *fakePinger) Ping(ctx context.Context, addrs []netip.Addr) (map[netip.Addr]time.Duration, error) {
	f.asked = addrs
	if f.during != nil {
		f.during()
	}
	return f.answer, f.err
}

// nmapSource is a fakeSource under nmap's name.
type nmapSource struct{ *fakeSource }

func (nmapSource) Name() string { return "nmap" }
func (n nmapSource) Collect(ctx context.Context, c config.Network) (model.Observations, error) {
	o, err := n.fakeSource.Collect(ctx, c)
	o.Links = nil
	return o, err
}

var ip1, ip2, ip3 = netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3")

func pingSetup(t *testing.T, p *fakePinger) (*Scheduler, *store.Store, *sink, Job) {
	s, st, sk, _ := setup(t, &fakeSource{})
	now := time.Now().UTC()
	dev := func(key string, ip netip.Addr, online bool) model.Device {
		return model.Device{Network: "lan", Key: key, IP: ip, Online: online, FirstSeen: now, LastSeen: now}
	}
	if err := st.UpsertDevices(context.Background(), []model.Device{dev("a", ip1, true), dev("b", ip2, true),
		dev("off", ip3, false), dev("v6", netip.MustParseAddr("fd00::1"), true)}); err != nil {
		t.Fatal(err)
	}
	j := Job{Network: lan, Ping: p, Interval: time.Hour}
	s = New(st, sk, nil, nil, []Job{j})
	return s, st, sk, j
}

func pingOf(t *testing.T, st *store.Store, key string) model.Ping {
	t.Helper()
	m, err := st.Pings(context.Background(), "lan")
	if err != nil {
		t.Fatal(err)
	}
	return m[model.MemberRef{Network: "lan", Key: key}]
}

func TestPingJobRecords(t *testing.T) {
	p := &fakePinger{answer: map[netip.Addr]time.Duration{ip1: 3 * time.Millisecond}}
	s, st, _, j := pingSetup(t, p)
	if err := s.RunOnce(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 2 || p.asked[0] != ip1 || p.asked[1] != ip2 {
		t.Fatalf("asked %v", p.asked)
	}
	if got := pingOf(t, st, "a"); !got.OK || got.RTT != 3*time.Millisecond || got.Origin != "gw" {
		t.Fatalf("a = %+v", got)
	}
	if got := pingOf(t, st, "b"); got.OK || got.Origin != "gw" {
		t.Fatalf("b = %+v", got)
	}
}

func TestPingJobNoEventsNoStateChange(t *testing.T) {
	s, st, sk, j := pingSetup(t, &fakePinger{})
	ctx := context.Background()
	before, _ := st.Devices(ctx, "lan")
	if err := s.RunOnce(ctx, j); err != nil {
		t.Fatal(err)
	}
	after, _ := st.Devices(ctx, "lan")
	for i := range before {
		if before[i].Online != after[i].Online || !before[i].LastSeen.Equal(after[i].LastSeen) {
			t.Fatalf("changed: %+v → %+v", before[i], after[i])
		}
	}
	evs, _ := st.Events(ctx, store.EventFilter{})
	if len(sk.evs) != 0 || len(evs) != 0 {
		t.Fatalf("events %v %v", sk.evs, evs)
	}
}

func TestPingJobKeyChangedMidRun(t *testing.T) {
	p := &fakePinger{answer: map[netip.Addr]time.Duration{ip1: time.Millisecond}}
	s, st, _, j := pingSetup(t, p)
	p.during = func() {
		st.UpsertDevices(context.Background(), []model.Device{{Network: "lan", Key: "new", PrevKey: "a", IP: ip1, Online: true,
			FirstSeen: time.Now(), LastSeen: time.Now()}})
	}
	if err := s.RunOnce(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if got := pingOf(t, st, "new"); !got.OK {
		t.Fatalf("new = %+v", got)
	}
}

func TestPingErrorOnlyOnPingJob(t *testing.T) {
	p := &fakePinger{err: errors.New("denied")}
	s, st, _, j := pingSetup(t, p)
	other := Job{Network: lan, Source: &fakeSource{}, Interval: time.Hour}
	s = New(st, nil, nil, nil, []Job{j, other})
	s.RunOnce(context.Background(), j)
	got := s.Status()
	if got[0].Source != "ping:gw" || got[0].LastErr != "denied" || got[1].LastErr != "" || !got[1].LastRun.IsZero() {
		t.Fatalf("status %+v", got)
	}
	if m, _ := st.Pings(context.Background(), ""); len(m) != 0 {
		t.Fatalf("a failed run recorded %v", m)
	}
}

func TestThresholdsIgnorePing(t *testing.T) {
	s, _, _, _ := pingSetup(t, &fakePinger{})
	if th := s.thresholds("lan"); len(th) != 0 {
		t.Fatalf("thresholds %v", th)
	}
	if err := s.ScanNow("lan"); !errors.Is(err, ErrUnknownNetwork) {
		t.Fatalf("ScanNow triggered a ping job: %v", err)
	}
}

func TestPingJobDoesNotTakeASlot(t *testing.T) {
	s, _, _, _ := pingSetup(t, &fakePinger{})
	for range MaxConcurrent {
		s.sem <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.runGuarded(ctx, 0)
	if s.Status()[0].LastOK.IsZero() {
		t.Fatal("ping job waited for a collection slot")
	}
}

func TestNmapRTTRecordedWithoutPingJob(t *testing.T) {
	src := nmapSource{&fakeSource{hosts: []model.Host{{MAC: "aa:00:00:00:00:01", IP: ip1, RTT: 8 * time.Millisecond},
		{MAC: "aa:00:00:00:00:02", IP: ip2}}}}
	_, st, _, _ := setup(t, src.fakeSource)
	j := Job{Network: lan, Source: src, Interval: time.Hour}
	s := New(st, nil, nil, nil, []Job{j})
	if err := s.RunOnce(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if got := pingOf(t, st, "mac:aa:00:00:00:00:01"); !got.OK || got.RTT != 8*time.Millisecond || got.Origin != "netmon" {
		t.Fatalf("ping = %+v", got)
	}
	if got := pingOf(t, st, "mac:aa:00:00:00:00:02"); got.OK || got.Origin != "netmon" {
		t.Fatalf("missed ping = %+v", got)
	}
}

func TestNmapRTTIgnoredWithPingJob(t *testing.T) {
	src := nmapSource{&fakeSource{hosts: []model.Host{{MAC: "aa:00:00:00:00:01", IP: ip1, RTT: 8 * time.Millisecond}}}}
	_, st, _, _ := setup(t, src.fakeSource)
	j := Job{Network: lan, Source: src, Interval: time.Hour}
	s := New(st, nil, nil, nil, []Job{j, {Network: lan, Ping: &fakePinger{}, Interval: time.Hour}})
	s.RunOnce(context.Background(), j)
	if m, _ := st.Pings(context.Background(), ""); len(m) != 0 {
		t.Fatalf("pings = %v", m)
	}
}
