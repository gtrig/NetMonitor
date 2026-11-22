package scheduler

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

type fakeSource struct {
	mu    sync.Mutex
	hosts []model.Host
	err   error
	calls int
}

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) Collect(ctx context.Context, n config.Network) (model.Observations, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return model.Observations{}, f.err
	}
	return model.Observations{Network: n.Name, Source: "fake", At: time.Now().UTC(), Hosts: f.hosts,
		Links: []model.Link{{A: model.NodeRef{Kind: "router", ID: "core"}, B: model.NodeRef{Kind: "router", ID: "ap"}}}}, nil
}

type sink struct {
	mu  sync.Mutex
	evs []model.Event
}

func (s *sink) Enqueue(evs []model.Event) { s.mu.Lock(); s.evs = append(s.evs, evs...); s.mu.Unlock() }

var lan = config.Network{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24")}

func setup(t *testing.T, src *fakeSource) (*Scheduler, *store.Store, *sink, Job) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sk := &sink{}
	j := Job{Network: lan, Source: src, Interval: time.Hour, OfflineAfter: 3 * time.Hour}
	return New(st, sk, nil, nil, []Job{j}), st, sk, j
}

func TestRunOncePersistsAndEnqueues(t *testing.T) {
	src := &fakeSource{hosts: []model.Host{
		{MAC: "aa:00:00:00:00:01", IP: netip.MustParseAddr("10.0.0.1")},
		{MAC: "aa:00:00:00:00:02", IP: netip.MustParseAddr("10.9.9.9")}, // outside CIDR
	}}
	s, st, sk, j := setup(t, src)
	ctx := context.Background()
	if err := s.RunOnce(ctx, j); err != nil {
		t.Fatal(err)
	}
	devs, _ := st.Devices(ctx, "lan")
	if len(devs) != 1 {
		t.Fatalf("devices = %+v", devs)
	}
	if len(sk.evs) != 1 || sk.evs[0].Type != model.EventBaseline {
		t.Fatalf("events = %+v", sk.evs)
	}
	stored, _ := st.Events(ctx, store.EventFilter{})
	if len(stored) != 1 {
		t.Fatalf("stored events = %+v", stored)
	}
	links, _ := st.Links(ctx, "lan")
	if len(links) != 1 {
		t.Fatalf("links = %+v", links)
	}
	// Second run with a new host: device_new.
	src.hosts = append(src.hosts, model.Host{MAC: "aa:00:00:00:00:03", IP: netip.MustParseAddr("10.0.0.3")})
	s.RunOnce(ctx, j)
	if len(sk.evs) != 2 || sk.evs[1].Type != model.EventDeviceNew {
		t.Fatalf("events = %+v", sk.evs)
	}
}

func TestRunOnceSourceErrorKeepsLastSuccess(t *testing.T) {
	src := &fakeSource{hosts: []model.Host{{MAC: "aa:00:00:00:00:01", IP: netip.MustParseAddr("10.0.0.1")}}}
	s, st, _, j := setup(t, src)
	ctx := context.Background()
	s.RunOnce(ctx, j)
	before, _ := st.LastSuccesses(ctx, "lan")
	src.err = errors.New("router down")
	if err := s.RunOnce(ctx, j); err == nil {
		t.Fatal("expected error")
	}
	after, _ := st.LastSuccesses(ctx, "lan")
	if !after["fake"].Equal(before["fake"]) {
		t.Fatalf("last success moved: %v → %v", before, after)
	}
	st0 := s.Status()
	if len(st0) != 1 || st0[0].LastErr != "router down" {
		t.Fatalf("status = %+v", st0)
	}
}

func TestScanNowUnknownNetwork(t *testing.T) {
	s, _, _, _ := setup(t, &fakeSource{})
	if err := s.ScanNow("nope"); !errors.Is(err, ErrUnknownNetwork) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunAndScanNow(t *testing.T) {
	src := &fakeSource{}
	s, _, _, _ := setup(t, src)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitCalls := func(n int) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			src.mu.Lock()
			c := src.calls
			src.mu.Unlock()
			if c >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("calls < %d", n)
	}
	waitCalls(1) // immediate first run
	if err := s.ScanNow("lan"); err != nil {
		t.Fatal(err)
	}
	waitCalls(2)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
