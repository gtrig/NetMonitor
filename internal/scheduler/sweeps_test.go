package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/source"
	"github.com/gtrig/netmon/internal/store"
)

type fakeSweeper struct {
	mu    sync.Mutex
	up    map[netip.Addr]bool
	tcp   map[netip.Addr]bool
	near  map[netip.Addr]source.Neighbor
	err   error
	calls int
}

func (f *fakeSweeper) Name() string { return "ping" }
func (f *fakeSweeper) Sweep(_ context.Context, addrs []netip.Addr, _ int, _ time.Duration) (map[netip.Addr]time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	out := map[netip.Addr]time.Duration{}
	for _, a := range addrs {
		if f.up[a] {
			out[a] = time.Millisecond
		}
	}
	return out, f.err
}
func (f *fakeSweeper) Reachable(_ context.Context, a netip.Addr) bool { return f.tcp[a] }
func (f *fakeSweeper) Neighbors() map[netip.Addr]source.Neighbor      { return f.near }

type sweepTest struct {
	*testing.T
	s  *Scheduler
	st *store.Store
	sk *sink
	f  *fakeSweeper
	j  Job
	sw *sweep
}

func host(n int) netip.Addr { return netip.AddrFrom4([4]byte{10, 0, 0, byte(n)}) }

func newSweepTest(t *testing.T, up ...int) *sweepTest {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fakeSweeper{up: map[netip.Addr]bool{}, tcp: map[netip.Addr]bool{}, near: map[netip.Addr]source.Neighbor{}}
	for _, n := range up {
		f.up[host(n)] = true
	}
	j := Job{Network: lan, Sweep: f, Interval: 10 * time.Second}
	sk := &sink{}
	s := New(st, sk, nil, nil, []Job{j})
	return &sweepTest{t, s, st, sk, f, j, s.sweeps["lan"]}
}

// sweep runs n sweeps, each one interval after the one before: it shifts the state back in time.
func (t *sweepTest) sweep(n int) {
	t.Helper()
	for range n {
		t.back(11 * time.Second)
		if _, err := t.s.sweepOnce(context.Background(), t.j); err != nil {
			t.Fatal(err)
		}
	}
}

func (t *sweepTest) back(d time.Duration) {
	t.sw.mu.Lock()
	defer t.sw.mu.Unlock()
	for _, h := range t.sw.hosts {
		h.clock = h.clock.Add(-d)
	}
	t.sw.heldSince = t.sw.heldSince.Add(-d)
}

func (t *sweepTest) device(n int) model.Device {
	t.Helper()
	devs, _ := t.st.Devices(context.Background(), "lan")
	for _, d := range devs {
		if d.IP == host(n) {
			return d
		}
	}
	t.Fatalf("no device %d in %+v", n, devs)
	return model.Device{}
}

func (t *sweepTest) online(n int, want bool) {
	t.Helper()
	if got := t.device(n).Online; got != want {
		t.Fatalf("device %d online = %v, want %v", n, got, want)
	}
}

func (t *sweepTest) events(typ string) int {
	n := 0
	for _, e := range t.sk.evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func (t *sweepTest) runSource(name string, hosts ...model.Host) {
	t.Helper()
	j := Job{Network: lan, Source: namedSource{&fakeSource{hosts: hosts}, name}, Interval: time.Hour, OfflineAfter: 3 * time.Hour}
	if err := t.s.RunOnce(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}

type namedSource struct {
	*fakeSource
	name string
}

func (n namedSource) Name() string { return n.name }

func TestSweepFindsAndLosesHosts(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	x.sweep(1)
	if x.events(model.EventBaseline) != 1 || x.events(model.EventDeviceNew) != 0 {
		t.Fatalf("events = %+v", x.sk.evs)
	}
	x.f.up[host(3)] = true
	x.sweep(1)
	if x.events(model.EventDeviceNew) != 1 {
		t.Fatalf("events = %+v", x.sk.evs)
	}
	delete(x.f.up, host(3))
	x.sweep(2)
	x.online(3, true)
	x.sweep(1)
	x.online(3, false)
	x.online(1, true)
	x.sweep(3)
	if x.events(model.EventDeviceOffline) != 1 {
		t.Fatalf("events = %+v", x.sk.evs)
	}
	x.f.up[host(3)] = true
	x.sweep(1)
	x.online(3, true)
	if x.events(model.EventDeviceBackOnline) != 1 {
		t.Fatalf("events = %+v", x.sk.evs)
	}
}

func TestSweepConfirmsBeforeOffline(t *testing.T) {
	for name, alive := range map[string]func(*sweepTest){
		"tcp": func(x *sweepTest) { x.f.tcp[host(2)] = true },
		"arp": func(x *sweepTest) {
			x.f.near[host(2)] = source.Neighbor{MAC: "aa:00:00:00:00:02", Confirmed: time.Now().Add(time.Hour)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newSweepTest(t, 1, 2)
			x.sweep(1)
			delete(x.f.up, host(2))
			alive(x)
			x.sweep(10)
			x.online(2, true)
			clear(x.f.tcp)
			x.f.near[host(2)] = source.Neighbor{MAC: "aa:00:00:00:00:02", Confirmed: time.Now().Add(-time.Hour)} // listed, but not since
			x.sweep(4)
			x.online(2, false)
		})
	}
}

func TestSweepBeatsOlderSightings(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	h := model.Host{IP: host(2), MAC: "aa:00:00:00:00:02"}
	x.runSource("nmap", h)
	x.runSource("router:core", h)
	x.sweep(1)
	delete(x.f.up, host(2))
	x.sweep(3)
	x.online(2, false)
	x.runSource("router:core", h) // a stale table entry
	x.online(2, false)
	// A scan that began before it went offline proves nothing.
	if got := x.s.withoutStale(model.Observations{Network: "lan", Source: "nmap", At: time.Now().UTC().Add(-time.Hour), Hosts: []model.Host{h}}, []model.Device{x.device(2)}); len(got) != 0 {
		t.Errorf("old scan kept %v", got)
	}
	x.runSource("nmap", h) // seen now, but silent to pings: back to the other sources' rules
	x.online(2, true)
	x.sweep(6)
	x.online(2, true)
	x.f.up[host(2)] = true // answers again: governed again
	x.sweep(1)
	delete(x.f.up, host(2))
	x.sweep(3)
	x.online(2, false)
}

func TestSweepKeepsRespondersOnline(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	x.runSource("nmap", model.Host{IP: host(2), MAC: "aa:00:00:00:00:02"})
	x.sweep(1)
	for range 3 { // nmap keeps missing it
		x.runSource("nmap")
	}
	x.online(2, true)
}

func TestSweepNeverGovernsSilentDevices(t *testing.T) {
	x := newSweepTest(t, 1)
	x.runSource("nmap", model.Host{IP: host(2), MAC: "aa:00:00:00:00:02"})
	x.sweep(10)
	x.online(2, true)
}

func TestSweepLearnsSilence(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	x.sweep(1)
	cycle := func() {
		delete(x.f.up, host(2))
		x.sweep(3)
		x.back(9 * time.Minute)
		x.f.up[host(2)] = true
		x.sweep(1)
	}
	for range 5 {
		cycle()
	}
	if n := x.events(model.EventDeviceOffline); n != 5 {
		t.Fatalf("%d offline events while learning", n)
	}
	delete(x.f.up, host(2))
	x.sweep(4)
	x.online(2, true)
	ref := model.MemberRef{Network: "lan", Key: x.device(2).Key}
	p := x.s.Presence([]model.Device{x.device(2)})[ref]
	if !p.Asleep || !p.Pattern.Periodic || p.Limit < 10*time.Minute {
		t.Fatalf("presence = %+v", p)
	}
	x.back(20 * time.Minute)
	x.sweep(1)
	x.online(2, false)
	// The gaps survive a restart.
	s := New(x.st, nil, nil, nil, []Job{x.j})
	if _, err := s.sweepOnce(context.Background(), x.j); err != nil {
		t.Fatal(err)
	}
	if p := s.Presence([]model.Device{x.device(2)})[ref]; !p.Pattern.Periodic {
		t.Errorf("after restart: %+v", p)
	}
}

func TestSweepOverride(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	x.sweep(1)
	x.st.SetOfflineAfter(context.Background(), x.device(2).Key, 10*time.Minute)
	delete(x.f.up, host(2))
	x.sweep(10)
	x.online(2, true)
	x.back(10 * time.Minute)
	x.sweep(1)
	x.online(2, false)
}

func TestSweepErrorsNeverMarkOffline(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	x.sweep(1)
	x.f.err = errors.New("network is unreachable")
	for range 5 {
		x.back(time.Minute)
		if _, err := x.s.sweepOnce(context.Background(), x.j); err == nil {
			t.Fatal("no error")
		}
	}
	x.f.err = nil
	delete(x.f.up, host(2))
	x.sweep(2)
	x.online(2, true) // its clock restarted
	x.sweep(2)
	x.online(2, false)
}

func TestSweepBrokenPath(t *testing.T) {
	all := []int{1, 2, 3, 4, 5, 6, 7, 8}
	for name, left := range map[string]int{"all gone": 0, "most gone": 3} {
		t.Run(name, func(t *testing.T) {
			x := newSweepTest(t, all...)
			x.sweep(1)
			for _, n := range all[left:] {
				delete(x.f.up, host(n))
			}
			x.sweep(10)
			x.online(8, true)
			for _, n := range all[:7] { // the path is back, but device 8 is not
				x.f.up[host(n)] = true
			}
			x.sweep(2)
			x.online(8, true)
			x.sweep(2)
			x.online(8, false)
			if gaps, _ := x.st.Gaps(context.Background(), "lan", time.Time{}); len(gaps) != 0 {
				t.Errorf("gaps of a broken path learned: %v", gaps)
			}
		})
	}
	x := newSweepTest(t, all...)
	x.sweep(1)
	clear(x.f.up)
	x.sweep(5)
	x.back(holdMax)
	x.sweep(1)
	if n := x.events(model.EventDeviceOffline); n != 8 {
		t.Fatalf("%d offline events after the hold", n)
	}
}

func TestSweepRestartGrace(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	x.sweep(1)
	delete(x.f.up, host(2))
	s := New(x.st, x.sk, nil, nil, []Job{x.j})
	y := &sweepTest{t, s, x.st, x.sk, x.f, x.j, s.sweeps["lan"]}
	y.sweep(2)
	y.online(2, true)
	y.sweep(3)
	y.online(2, false)
}

func TestSweepFollowsKeyChange(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	x.sweep(1)
	x.runSource("router:core", model.Host{IP: host(2), MAC: "aa:00:00:00:00:02"})
	if d := x.device(2); d.Key != "mac:aa:00:00:00:00:02" {
		t.Fatalf("%+v", d)
	}
	if x.sw.hosts["mac:aa:00:00:00:00:02"] == nil || x.sw.hosts["ip:10.0.0.2"] != nil {
		t.Errorf("state not moved")
	}
	delete(x.f.up, host(2))
	x.sweep(3)
	x.online(2, false)
}

func TestSweepWritesLittle(t *testing.T) {
	x := newSweepTest(t, 1, 2)
	x.sweep(1)
	seen := func() time.Time { return x.device(1).SeenBy["ping"] }
	first := seen()
	time.Sleep(1100 * time.Millisecond)
	x.sweep(3)
	if !seen().Equal(first) {
		t.Error("last seen written by every sweep")
	}
	x.sw.flushed = time.Time{}
	x.sweep(1)
	if !seen().After(first) {
		t.Error("last seen not flushed")
	}
}

func TestIdentify(t *testing.T) {
	now := time.Now()
	known := []model.Device{
		{Key: "mac:aa:00:00:00:00:01", MAC: "aa:00:00:00:00:01", IP: host(1), LastSeen: now.Add(-time.Hour)},
		{Key: "mac:aa:00:00:00:00:02", MAC: "aa:00:00:00:00:02", IP: host(1), LastSeen: now},
		{Key: "mac:aa:00:00:00:00:05", MAC: "aa:00:00:00:00:05", IP: host(5), LastSeen: now},
	}
	replies := map[netip.Addr]time.Duration{host(1): 1, host(4): 1, host(5): 1, host(9): 1}
	got := identify(known, replies, map[netip.Addr]source.Neighbor{host(4): {MAC: "aa:00:00:00:00:05"}, host(5): {MAC: "aa:00:00:00:00:05"}})
	if len(got) != 3 || got[0].MAC != "aa:00:00:00:00:02" || got[1].IP != host(5) || got[2].MAC != "" {
		t.Errorf("%+v", got)
	}
}

func TestRunSweepsHistoryAndTrigger(t *testing.T) {
	x := newSweepTest(t, 1)
	x.s.jobs[0].Interval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { x.s.Run(ctx); close(done) }()
	wait := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); !ok(); time.Sleep(2 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("timeout: " + what)
			}
		}
	}
	runs := func() string {
		rs, _ := x.st.JobRuns(context.Background(), store.JobRunFilter{})
		out := ""
		for _, r := range rs {
			out = fmt.Sprintf("%s/%s ", r.Trigger, r.Outcome) + out
		}
		return out
	}
	calls := func(n int) func() bool {
		return func() bool { x.f.mu.Lock(); defer x.f.mu.Unlock(); return x.f.calls > n }
	}
	setErr := func(err error) { x.f.mu.Lock(); x.f.err = err; x.f.mu.Unlock() }
	wait("sweeps", calls(5))
	if r := runs(); r != "" {
		t.Errorf("ordinary sweeps in history: %s", r)
	}
	setErr(errors.New("down"))
	wait("failure", func() bool { return runs() == "scheduled/failed " })
	time.Sleep(30 * time.Millisecond)
	setErr(nil)
	wait("recovery", func() bool { return runs() == "scheduled/failed scheduled/ok " })
	if err := x.s.ScanNow("lan"); err != nil {
		t.Fatal(err)
	}
	wait("manual", func() bool { return runs() == "scheduled/failed scheduled/ok manual/ok " })
	cancel()
	<-done
}

func TestSweepNetworkTooLarge(t *testing.T) {
	j := Job{Network: config.Network{Name: "big", CIDR: netip.MustParsePrefix("10.0.0.0/8")}, Sweep: &fakeSweeper{}, Interval: time.Second}
	s := New(nil, nil, nil, nil, []Job{j})
	if _, err := s.sweepOnce(context.Background(), j); err == nil {
		t.Error("accepted")
	}
}
