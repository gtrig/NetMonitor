package scheduler

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

func TestQueueOrderAndLimits(t *testing.T) {
	q := &queue{scans: 2, detailed: 1}
	ctx := context.Background()
	d1 := q.enter("d1", true, PrioAuto)
	d2 := q.enter("d2", true, PrioAuto)
	d3 := q.enter("d3", true, PrioAuto)
	p1 := q.enter("p1", false, PrioPresence) // overtakes the detailed scans beyond their limit
	p2 := q.enter("p2", false, PrioPresence)
	if !d1.granted || d2.granted || !p1.granted || p2.granted {
		t.Fatal("wrong entries granted")
	}
	if !q.promote("d3", PrioManual) || q.promote("d1", PrioManual) {
		t.Fatal("promote must only find waiting entries")
	}
	if pl := q.places(); pl["d3"].pos != 1 || pl["p2"].pos != 2 || pl["d2"].pos != 3 {
		t.Fatalf("places = %v", pl)
	}
	q.release(d1)
	if !d3.granted || d2.granted || p2.granted {
		t.Fatal("manual scan did not go first")
	}
	if !q.cancel("p2") || q.cancel("p2") {
		t.Fatal("cancel")
	}
	if err := q.wait(ctx, p2); !errors.Is(err, ErrCancelled) {
		t.Fatalf("wait = %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := q.wait(cctx, d2); err == nil || len(q.waiting) != 0 {
		t.Fatalf("stopped wait = %v, %d waiting", err, len(q.waiting))
	}
	q.release(d3)
	if err := q.wait(cctx, p1); err == nil || q.running != 0 { // stopping gives the slot back
		t.Fatalf("running = %d", q.running)
	}
}

// blockingDetails waits for release on every scan.
type blockingDetails struct {
	started chan netip.Addr
	release chan error
}

func (b *blockingDetails) Name() string { return "services" }
func (b *blockingDetails) Scan(ctx context.Context, ts []netip.Addr) ([]model.HostDetails, error) {
	b.started <- ts[0]
	select {
	case err := <-b.release:
		return []model.HostDetails{{IP: ts[0]}}, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func autoSched(t *testing.T, n config.Network, d DetailScanner, hosts int) *Scheduler {
	t.Helper()
	src := &fakeSource{}
	for i := range hosts {
		src.hosts = append(src.hosts, model.Host{MAC: "aa:00:00:00:00:0" + string(rune('1'+i)), IP: netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)})})
	}
	_, st, _, _ := setup(t, src)
	s := New(st, nil, nil, nil, []Job{{Network: n, Source: src, Interval: time.Hour, OfflineAfter: time.Hour}})
	s.DefaultDetails = d
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

func presenceRuns(s *Scheduler, n int) func() bool {
	return func() bool {
		src := s.jobs[0].Source.(*fakeSource)
		src.mu.Lock()
		defer src.mu.Unlock()
		return src.calls >= n
	}
}

func idle(s *Scheduler) func() bool {
	return func() bool { st := s.Status(); return len(st) == 1 && st[0].State == StateIdle }
}

func TestAutoDetailsScansUnscannedOnce(t *testing.T) {
	fd := &fakeDetails{}
	s := autoSched(t, lan, fd, 3)
	waitFor(t, func() bool { d, _ := s.st.Details(context.Background(), "lan"); return len(d) == 3 })
	waitFor(t, idle(s))
	s.ScanJob(0)
	waitFor(t, presenceRuns(s, 2))
	waitFor(t, idle(s))
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.targets) != 3 {
		t.Fatalf("scans = %v", fd.targets)
	}
}

func TestAutoDetailsOff(t *testing.T) {
	n, off, fd := lan, false, &fakeDetails{}
	n.AutoDetails = &off
	s := autoSched(t, n, fd, 2)
	waitFor(t, presenceRuns(s, 1))
	waitFor(t, idle(s))
	if len(fd.targets) != 0 {
		t.Fatalf("scans = %v", fd.targets)
	}
}

func TestQueuedDeviceScans(t *testing.T) {
	b := &blockingDetails{started: make(chan netip.Addr, 9), release: make(chan error, 9)}
	s := autoSched(t, lan, b, 5)
	<-b.started
	<-b.started
	waitFor(t, func() bool { return len(s.queue.places()) == 3 })
	var waiting []JobStatus
	for _, st := range s.Status() {
		if st.Position > 0 {
			waiting = append(waiting, st)
		}
	}
	if len(waiting) != 3 || waiting[0].Position != 1 || waiting[0].Priority != "automatic" {
		t.Fatalf("waiting = %+v", waiting)
	}
	// A manual request moves the waiting scan up, without a second entry.
	last := waiting[2]
	if err := s.ScanDevice("lan", last.QueueID[len("device:lan|"):]); err != nil {
		t.Fatal(err)
	}
	if p := s.queue.places(); len(p) != 3 || p[last.QueueID] != (place{1, PrioManual}) {
		t.Fatalf("places = %v", p)
	}
	// Cancelling drops it, and the next presence scan does not queue it again.
	if err := s.Cancel(waiting[0].QueueID); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(waiting[0].QueueID); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("second cancel = %v", err)
	}
	b.release <- errors.New("boom") // one failure, the rest succeed
	for range 3 {
		b.release <- nil
	}
	waitFor(t, idle(s))
	d, _ := s.st.Details(context.Background(), "lan")
	if len(d) != 3 {
		t.Fatalf("details = %v", d)
	}
	s.ScanJob(0)
	waitFor(t, presenceRuns(s, 2))
	waitFor(t, idle(s)) // neither the failed nor the cancelled device is retried yet
	if n := len(s.retries); n != 2 {
		t.Fatalf("retries = %v", s.retries)
	}
}

func TestPresenceRunsWhileDetailedScansBlock(t *testing.T) {
	b := &blockingDetails{started: make(chan netip.Addr, 9), release: make(chan error, 9)}
	s := autoSched(t, lan, b, 5)
	<-b.started
	<-b.started
	s.ScanJob(0)
	waitFor(t, presenceRuns(s, 2))
}

func TestDetailsJobResumesAndReportsFailures(t *testing.T) {
	_, st, _, _ := setup(t, &fakeSource{})
	b := &blockingDetails{started: make(chan netip.Addr, 9), release: make(chan error, 9)}
	j := Job{Network: lan, Details: b, Interval: 6 * time.Hour}
	s := New(st, nil, nil, nil, []Job{j})
	now := time.Now().UTC()
	ctx := context.Background()
	st.UpsertDevices(ctx, []model.Device{
		{Network: "lan", Key: "mac:aa", IP: netip.MustParseAddr("10.0.0.2"), Online: true, FirstSeen: now, LastSeen: now},
		{Network: "lan", Key: "mac:bb", IP: netip.MustParseAddr("10.0.0.3"), Online: true, FirstSeen: now, LastSeen: now}})
	b.release <- errors.New("boom")
	b.release <- nil
	if err := s.RunOnce(ctx, j); err == nil {
		t.Fatal("failure not reported")
	}
	if last, _ := st.LastSuccesses(ctx, "lan"); last["services"].IsZero() {
		t.Fatal("partly successful run not recorded")
	}
	b.release <- nil // only the failed device is left
	if err := s.RunOnce(ctx, j); err != nil || len(b.started) != 3 {
		t.Fatalf("err = %v, scans = %d", err, len(b.started))
	}
	b.release <- nil
	b.release <- nil
	if err := s.run(ctx, j, PrioManual); err != nil || len(b.started) != 5 { // manual rescans everything
		t.Fatalf("err = %v, scans = %d", err, len(b.started))
	}
}

// renamingDetails changes the device's key while it is being scanned.
type renamingDetails struct{ s *Scheduler }

func (r renamingDetails) Name() string { return "services" }
func (r renamingDetails) Scan(ctx context.Context, ts []netip.Addr) ([]model.HostDetails, error) {
	devs, _ := r.s.st.Devices(ctx, "lan")
	d := devs[0]
	r.s.st.DeleteDevice(ctx, "lan", d.Key)
	d.Key = "mac:new"
	r.s.st.UpsertDevices(ctx, []model.Device{d})
	return []model.HostDetails{{IP: ts[0]}}, nil
}

func TestResultFollowsChangedKey(t *testing.T) {
	_, st, _, _ := setup(t, &fakeSource{})
	s := New(st, nil, nil, nil, nil)
	now := time.Now().UTC()
	st.UpsertDevices(context.Background(), []model.Device{{Network: "lan", Key: "ip:10.0.0.2", IP: netip.MustParseAddr("10.0.0.2"), Online: true, FirstSeen: now, LastSeen: now}})
	if err := s.RunOnce(context.Background(), Job{Network: lan, Details: renamingDetails{s}, Interval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	d, _ := st.Details(context.Background(), "lan")
	if _, ok := d[model.MemberRef{Network: "lan", Key: "mac:new"}]; !ok || len(d) != 1 {
		t.Fatalf("details = %v", d)
	}
}
