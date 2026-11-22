// Package scheduler runs sources on their intervals and pushes results through the pipeline.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/diff"
	"github.com/gtrig/netmon/internal/enrich"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/source"
	"github.com/gtrig/netmon/internal/store"
)

// ErrUnknownNetwork is returned by ScanNow for a network that has no jobs.
var ErrUnknownNetwork = errors.New("unknown network")

// MaxConcurrent is the maximum number of collections running at once.
const MaxConcurrent = 4

// EventSink receives new events (the notification dispatcher).
type EventSink interface{ Enqueue([]model.Event) }

// Job is one source collecting one network.
type Job struct {
	Network      config.Network
	Source       source.Source
	Interval     time.Duration
	OfflineAfter time.Duration
}

// JobStatus describes the last run of a job.
type JobStatus struct {
	Network  string    `json:"network"`
	Source   string    `json:"source"`
	Interval string    `json:"interval"`
	LastRun  time.Time `json:"lastRun"`
	LastOK   time.Time `json:"lastOK"`
	LastErr  string    `json:"lastErr,omitempty"`
	Running  bool      `json:"running"`
}

// Scheduler runs jobs.
type Scheduler struct {
	st      *store.Store
	sink    EventSink
	oui     *enrich.OUI
	res     *enrich.Resolver
	jobs    []Job
	trigger []chan struct{}
	sem     chan struct{}

	mu      sync.Mutex
	status  []JobStatus
	netLock map[string]*sync.Mutex
}

// New returns a scheduler. oui and res may be nil.
func New(st *store.Store, sink EventSink, oui *enrich.OUI, res *enrich.Resolver, jobs []Job) *Scheduler {
	s := &Scheduler{st: st, sink: sink, oui: oui, res: res, jobs: jobs, sem: make(chan struct{}, MaxConcurrent),
		netLock: map[string]*sync.Mutex{}}
	for _, j := range jobs {
		s.trigger = append(s.trigger, make(chan struct{}, 1))
		s.status = append(s.status, JobStatus{Network: j.Network.Name, Source: j.Source.Name(), Interval: j.Interval.String()})
		if s.netLock[j.Network.Name] == nil {
			s.netLock[j.Network.Name] = &sync.Mutex{}
		}
	}
	return s
}

// Run starts every job (immediately, then every Interval) and blocks until ctx is done
// and in-flight collections have finished.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := range s.jobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j := s.jobs[i]
			t := time.NewTicker(j.Interval)
			defer t.Stop()
			for {
				s.runGuarded(ctx, i)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				case <-s.trigger[i]:
					t.Reset(j.Interval)
				}
			}
		}(i)
	}
	wg.Wait()
}

func (s *Scheduler) runGuarded(ctx context.Context, i int) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.sem }()
	if err := s.RunOnce(ctx, s.jobs[i]); err != nil && ctx.Err() == nil {
		slog.Warn("collection failed", "network", s.jobs[i].Network.Name, "source", s.jobs[i].Source.Name(), "err", err)
	}
}

// ScanNow triggers all jobs of network without waiting for them.
func (s *Scheduler) ScanNow(network string) error {
	found := false
	for i, j := range s.jobs {
		if j.Network.Name == network {
			found = true
			select {
			case s.trigger[i] <- struct{}{}:
			default: // already pending
			}
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrUnknownNetwork, network)
	}
	return nil
}

// Status returns a snapshot of every job's last run.
func (s *Scheduler) Status() []JobStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]JobStatus(nil), s.status...)
}

func (s *Scheduler) setStatus(j Job, f func(*JobStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.status {
		if s.jobs[i].Source == j.Source && s.jobs[i].Network.Name == j.Network.Name {
			f(&s.status[i])
		}
	}
}

// thresholds returns per-source offline thresholds for every job of network.
func (s *Scheduler) thresholds(network string) map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, j := range s.jobs {
		if j.Network.Name == network {
			out[j.Source.Name()] = j.OfflineAfter
		}
	}
	return out
}

// RunOnce runs one collection through the whole pipeline.
func (s *Scheduler) RunOnce(ctx context.Context, j Job) error {
	s.setStatus(j, func(st *JobStatus) { st.Running = true; st.LastRun = time.Now().UTC() })
	err := s.runOnce(ctx, j)
	s.setStatus(j, func(st *JobStatus) {
		st.Running = false
		if err != nil {
			st.LastErr = err.Error()
		} else {
			st.LastErr = ""
			st.LastOK = time.Now().UTC()
		}
	})
	return err
}

func (s *Scheduler) runOnce(ctx context.Context, j Job) error {
	obs, err := j.Source.Collect(ctx, j.Network)
	if err != nil {
		return err
	}
	if obs.At.IsZero() {
		obs.At = time.Now().UTC()
	}
	obs.Source = j.Source.Name()
	obs = source.Merge(j.Network, obs)
	enrich.Enrich(&obs, s.oui, s.res)

	lock := s.netLock[j.Network.Name]
	if lock == nil {
		lock = &sync.Mutex{}
	}
	lock.Lock()
	defer lock.Unlock()

	name := j.Network.Name
	known, err := s.st.Devices(ctx, name)
	if err != nil {
		return err
	}
	last, err := s.st.LastSuccesses(ctx, name)
	if err != nil {
		return err
	}
	_, seenBefore := last[obs.Source]
	changed, events := diff.Apply(known, obs, !seenBefore)
	if err := s.st.UpsertDevices(ctx, changed); err != nil {
		return err
	}
	if err := s.st.ReplaceLinks(ctx, name, obs.Source, obs.Links); err != nil {
		return err
	}
	if err := s.st.SetLastSuccess(ctx, name, obs.Source, obs.At); err != nil {
		return err
	}
	last[obs.Source] = obs.At

	known, err = s.st.Devices(ctx, name)
	if err != nil {
		return err
	}
	off, offEvents := diff.Offline(known, name, last, s.thresholds(name))
	if err := s.st.UpsertDevices(ctx, off); err != nil {
		return err
	}
	events = append(events, offEvents...)
	if err := s.st.AddEvents(ctx, events); err != nil {
		return err
	}
	if len(events) > 0 && s.sink != nil {
		s.sink.Enqueue(events)
	}
	return nil
}
