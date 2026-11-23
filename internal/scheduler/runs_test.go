package scheduler

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

func runsOf(t *testing.T, s *Scheduler, kind string) []model.JobRun {
	t.Helper()
	r, err := s.st.JobRuns(context.Background(), store.JobRunFilter{Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPresenceRunsRecorded(t *testing.T) {
	src := &fakeSource{hosts: []model.Host{{MAC: "aa:00:00:00:00:01", IP: netip.MustParseAddr("10.0.0.1")}}}
	s, _, _, j := setup(t, src)
	s.RunOnce(context.Background(), j)
	src.err = errors.New("login failed")
	s.RunOnce(context.Background(), j)
	r := runsOf(t, s, model.RunPresence)
	if len(r) != 2 || r[1].Outcome != model.OutcomeOK || r[1].Message != "1 host seen, 1 change" || r[1].Trigger != "scheduled" ||
		r[0].Outcome != model.OutcomeFailed || r[0].Message != "login failed" {
		t.Fatalf("runs = %+v", r)
	}
}

func TestDeviceRunsRecorded(t *testing.T) {
	b := &blockingDetails{started: make(chan netip.Addr, 9), release: make(chan error, 9)}
	s := autoSched(t, lan, b, 4)
	<-b.started
	<-b.started
	waitFor(t, func() bool { return len(s.queue.places()) == 2 })
	for id, p := range s.queue.places() {
		if p.pos == 2 {
			s.Cancel(id)
		}
	}
	b.release <- errors.New("boom")
	b.release <- nil
	b.release <- nil
	waitFor(t, idle(s))
	got := map[string]int{}
	for _, r := range runsOf(t, s, model.RunDevice) {
		got[r.Outcome]++
		if r.Trigger != "automatic" || r.Key == "" || (r.Outcome == model.OutcomeOK && r.Message != "0 open ports") {
			t.Errorf("run = %+v", r)
		}
	}
	if got[model.OutcomeOK] != 2 || got[model.OutcomeFailed] != 1 || got[model.OutcomeCancelled] != 1 {
		t.Fatalf("outcomes = %v", got)
	}
}

func TestInterruptedRunRecorded(t *testing.T) {
	b := &blockingDetails{started: make(chan netip.Addr, 9), release: make(chan error)}
	s, _, _, _ := setup(t, &fakeSource{})
	seed(t, s)
	s.DefaultDetails = b
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	<-b.started
	cancel()
	<-done
	r := runsOf(t, s, model.RunDevice)
	if len(r) != 1 || r[0].Outcome != model.OutcomeInterrupted || r[0].Duration < 0 || time.Since(r[0].At) > time.Minute {
		t.Fatalf("runs = %+v", r)
	}
}

func TestNetworkDetailsRunRecorded(t *testing.T) {
	s, _, _, _ := setup(t, &fakeSource{})
	seed(t, s)
	j := Job{Network: lan, Details: &fakeDetails{}, Interval: time.Hour}
	s.run(context.Background(), j, PrioManual)
	r := runsOf(t, s, model.RunDetails)
	if len(r) != 1 || r[0].Message != "1 device scanned" || r[0].Trigger != "manual" {
		t.Fatalf("runs = %+v", r)
	}
	if r := runsOf(t, s, model.RunDevice); len(r) != 1 || r[0].Message != "1 open port, Linux" {
		t.Fatalf("device runs = %+v", r)
	}
}
