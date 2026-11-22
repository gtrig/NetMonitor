package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/source"
	"github.com/gtrig/netmon/internal/store"
)

// blockingSource reports 40% progress, then waits for release.
type blockingSource struct {
	name    string
	started chan struct{}
	release chan struct{}
}

func (b *blockingSource) Name() string { return b.name }
func (b *blockingSource) Collect(ctx context.Context, n config.Network) (model.Observations, error) {
	source.ReportProgress(ctx, source.Progress{Task: "Ping Scan", Percent: 40})
	b.started <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return model.Observations{Network: n.Name, Source: b.name, At: time.Now().UTC()}, nil
}

func newBlocking(name string) *blockingSource {
	return &blockingSource{name: name, started: make(chan struct{}, 1), release: make(chan struct{})}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runSched(t *testing.T, jobs []Job) *Scheduler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil, nil, nil, jobs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

func TestJobStateAndProgress(t *testing.T) {
	src := newBlocking("b")
	s := runSched(t, []Job{{Network: lan, Source: src, Interval: time.Hour, OfflineAfter: 3 * time.Hour}})
	<-src.started
	st := s.Status()[0]
	if st.State != StateRunning || st.Progress == nil || *st.Progress != 40 || st.Task != "Ping Scan" || st.StartedAt.IsZero() {
		t.Fatalf("running status = %+v", st)
	}
	close(src.release)
	waitFor(t, func() bool { return s.Status()[0].State == StateIdle })
	st = s.Status()[0]
	if st.Progress != nil || st.Task != "" || st.LastOK.IsZero() {
		t.Fatalf("idle status = %+v", st)
	}
	if d := time.Until(st.NextRun); d < 59*time.Minute || d > time.Hour {
		t.Fatalf("next run in %v", d)
	}
}

func TestQueuedWhileSemaphoreFull(t *testing.T) {
	var jobs []Job
	var srcs []*blockingSource
	for i := 0; i <= MaxConcurrent; i++ {
		src := newBlocking(string(rune('a' + i)))
		srcs = append(srcs, src)
		jobs = append(jobs, Job{Network: lan, Source: src, Interval: time.Hour, OfflineAfter: time.Hour})
	}
	s := runSched(t, jobs)
	waitFor(t, func() bool {
		running, queued := 0, 0
		for _, st := range s.Status() {
			switch st.State {
			case StateRunning:
				running++
			case StateQueued:
				queued++
			}
		}
		return running == MaxConcurrent && queued == 1
	})
	for _, src := range srcs {
		close(src.release)
	}
	waitFor(t, func() bool {
		for _, st := range s.Status() {
			if st.State != StateIdle {
				return false
			}
		}
		return true
	})
}

func TestScanJob(t *testing.T) {
	s, _, _, _ := setup(t, &fakeSource{})
	if err := s.ScanJob(7); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("err = %v", err)
	}
	if err := s.ScanJob(0); err != nil {
		t.Fatal(err)
	}
	if s.Status()[0].ID != 0 {
		t.Fatal("job id")
	}
}
