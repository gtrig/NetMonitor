// Package scheduler runs sources on their intervals and pushes results through the pipeline.
package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
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

// ErrUnknownDevice is returned by ScanDevice for a device that is not stored.
var ErrUnknownDevice = errors.New("unknown device")

// ErrUnknownJob is returned by ScanJob for a job id that does not exist.
var ErrUnknownJob = errors.New("unknown job")

// Job states.
const (
	StateIdle    = "idle"
	StateQueued  = "queued" // waiting for a free collection slot
	StateRunning = "running"
)

// EventSink receives new events (the notification dispatcher).
type EventSink interface{ Enqueue([]model.Event) }

// DetailScanner runs detailed (services) scans of given addresses.
type DetailScanner interface {
	Name() string
	Scan(ctx context.Context, targets []netip.Addr) ([]model.HostDetails, error)
}

// Job is one source collecting one network: presence (Source), details (Details) or ping times (Ping).
type Job struct {
	Network      config.Network
	Source       source.Source
	Details      DetailScanner
	Ping         Pinger
	Interval     time.Duration
	OfflineAfter time.Duration
}

// Name is the job's source name.
func (j Job) Name() string {
	if j.Details != nil {
		return j.Details.Name()
	}
	if j.Ping != nil {
		return j.Ping.Name()
	}
	return j.Source.Name()
}

// JobStatus describes a job: its current state and its last run.
type JobStatus struct {
	ID           int       `json:"id"` // -1: a detailed scan of one device
	QueueID      string    `json:"queueId"`
	Position     int       `json:"position,omitempty"` // in the queue, 1 = next
	Priority     string    `json:"priority,omitempty"` // while queued or running
	Network      string    `json:"network"`
	Source       string    `json:"source"`
	Interval     string    `json:"interval"`
	State        string    `json:"state"`
	StartedAt    time.Time `json:"startedAt"`
	NextRun      time.Time `json:"nextRun"`
	Progress     *float64  `json:"progress,omitempty"` // percent, when the source reports it
	Task         string    `json:"task,omitempty"`
	LastRun      time.Time `json:"lastRun"`
	LastOK       time.Time `json:"lastOK"`
	LastDuration float64   `json:"lastDuration"` // seconds
	LastErr      string    `json:"lastErr,omitempty"`
	Running      bool      `json:"running"`
}

// Scheduler runs jobs.
type Scheduler struct {
	st      *store.Store
	sink    EventSink
	oui     *enrich.OUI
	res     *enrich.Resolver
	jobs    []Job
	trigger []chan struct{}
	queue   *queue

	mu       sync.Mutex
	status   []JobStatus
	netLock  map[string]*sync.Mutex
	devices  map[string]*deviceScan // queued and running detailed scans, by deviceID
	retries  map[string]retry       // failed or cancelled detailed scans, by deviceID
	deviceWG sync.WaitGroup
	stopped  bool
	runCtx   context.Context // set while Run is active

	// DefaultDetails scans devices on demand in networks without a services job.
	DefaultDetails DetailScanner
}

// New returns a scheduler. oui and res may be nil.
func New(st *store.Store, sink EventSink, oui *enrich.OUI, res *enrich.Resolver, jobs []Job) *Scheduler {
	s := &Scheduler{st: st, sink: sink, oui: oui, res: res, jobs: jobs, queue: &queue{scans: config.DefaultScans, detailed: config.DefaultDetailed},
		netLock: map[string]*sync.Mutex{}, devices: map[string]*deviceScan{}, retries: map[string]retry{}}
	for i, j := range jobs {
		s.trigger = append(s.trigger, make(chan struct{}, 1))
		s.status = append(s.status, JobStatus{ID: i, QueueID: jobID(i, j), Network: j.Network.Name, Source: j.Name(),
			Interval: humanDuration(j.Interval), State: StateIdle})
		if s.netLock[j.Network.Name] == nil {
			s.netLock[j.Network.Name] = &sync.Mutex{}
		}
	}
	return s
}

// SetWorkers sets how many scans run in parallel, and how many of them may be detailed scans.
// Call it before Run.
func (s *Scheduler) SetWorkers(w config.Workers) {
	s.queue.scans, s.queue.detailed = w.Scans, w.Detailed
}

func jobID(i int, j Job) string { return fmt.Sprintf("job:%d:%s:%s", i, j.Network.Name, j.Name()) }

// Run starts every job (immediately, then every Interval) and blocks until ctx is done
// and in-flight collections have finished.
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
	var wg sync.WaitGroup
	defer func() {
		s.mu.Lock()
		s.stopped = true // no new device scans from here on
		s.mu.Unlock()
		s.deviceWG.Wait()
	}()
	for i := range s.jobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j := s.jobs[i]
			manual := false
			t := time.NewTicker(j.Interval)
			defer t.Stop()
			if wait := s.firstDelay(ctx, j); wait > 0 {
				next := time.Now().UTC().Add(wait)
				s.setStatusAt(i, func(st *JobStatus) {
					st.NextRun, st.LastOK, st.LastRun = next, next.Add(-j.Interval), next.Add(-j.Interval)
				})
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				case <-s.trigger[i]:
					timer.Stop()
					manual = true
				}
			}
			for {
				s.runGuarded(ctx, i, manual)
				manual = false
				t.Reset(j.Interval)
				next := time.Now().UTC().Add(j.Interval)
				s.setStatusAt(i, func(st *JobStatus) { st.NextRun = next })
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				case <-s.trigger[i]:
					manual = true
				}
			}
		}(i)
	}
	<-ctx.Done() // also with no jobs: on-demand device scans need a running scheduler
	wg.Wait()
}

func (s *Scheduler) runGuarded(ctx context.Context, i int, manual bool) {
	j, prio := s.jobs[i], PrioPresence
	switch {
	case manual:
		prio = PrioManual
	case j.Details != nil:
		prio = PrioScheduled
	}
	// Pings are light and must not hold up collections; detailed jobs queue their devices.
	if j.Source != nil {
		e := s.queue.enter(jobID(i, j), false, prio)
		s.setStatusAt(i, func(st *JobStatus) { st.State, st.Priority = StateQueued, prio.String() })
		if err := s.queue.wait(ctx, e); err != nil {
			s.record(ctx, model.JobRun{Network: j.Network.Name, Kind: j.kind(), Source: j.Name(), Trigger: s.queue.priority(e).trigger()}, time.Time{}, err)
			s.setStatusAt(i, func(st *JobStatus) { st.State, st.Priority = StateIdle, "" })
			return
		}
		defer s.queue.release(e)
		prio = s.queue.priority(e)
		s.setStatusAt(i, func(st *JobStatus) { st.Priority = prio.String() })
	}
	if err := s.run(ctx, j, prio); err != nil && ctx.Err() == nil {
		slog.Warn("collection failed", "network", j.Network.Name, "source", j.Name(), "err", err)
	}
}

// ScanNow triggers the presence jobs of network without waiting for them (detailed scans run
// on their own schedule or per device).
func (s *Scheduler) ScanNow(network string) error {
	found := false
	for i, j := range s.jobs {
		if j.Network.Name == network && j.Source != nil {
			found = true
			s.triggerJob(i)
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrUnknownNetwork, network)
	}
	return nil
}

// ScanJob triggers one job by id without waiting for it.
func (s *Scheduler) ScanJob(id int) error {
	if id < 0 || id >= len(s.jobs) {
		return fmt.Errorf("%w: %d", ErrUnknownJob, id)
	}
	s.triggerJob(id)
	return nil
}

// triggerJob runs job i next. A waiting job moves up instead of running twice, and a running
// detailed job already covers the request.
func (s *Scheduler) triggerJob(i int) {
	s.mu.Lock()
	busy := s.jobs[i].Details != nil && s.status[i].Running
	s.mu.Unlock()
	if busy || s.queue.promote(jobID(i, s.jobs[i]), PrioManual) {
		return
	}
	select {
	case s.trigger[i] <- struct{}{}:
	default: // already pending
	}
}

// Status returns a snapshot of every job and of the queued and running device scans.
func (s *Scheduler) Status() []JobStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]JobStatus(nil), s.status...)
	for _, d := range s.devices {
		out = append(out, *d.status)
	}
	places := s.queue.places()
	for i := range out {
		if p, ok := places[out[i].QueueID]; ok {
			out[i].Position, out[i].Priority = p.pos, p.prio.String()
		}
		if p := out[i].Progress; p != nil {
			v := *p
			out[i].Progress = &v
		}
	}
	devs := out[len(s.status):] // map order: sort by queue position
	slices.SortFunc(devs, func(a, b JobStatus) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), a.StartedAt.Compare(b.StartedAt), cmp.Compare(a.QueueID, b.QueueID))
	})
	return out
}

func (s *Scheduler) setStatusAt(i int, f func(*JobStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.status[i])
}

func (s *Scheduler) setStatus(j Job, f func(*JobStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.status {
		if s.jobs[i].Source == j.Source && s.jobs[i].Details == j.Details && s.jobs[i].Ping == j.Ping && s.jobs[i].Network.Name == j.Network.Name {
			f(&s.status[i])
		}
	}
}

// thresholds returns per-source offline thresholds for every job of network.
func (s *Scheduler) thresholds(network string) map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, j := range s.jobs {
		if j.Network.Name == network && j.Source != nil {
			out[j.Source.Name()] = j.OfflineAfter
		}
	}
	return out
}

// RunOnce runs one collection through the whole pipeline, as if scheduled.
func (s *Scheduler) RunOnce(ctx context.Context, j Job) error {
	return s.run(ctx, j, PrioScheduled)
}

// run runs j at once; prio is for the device scans a details job queues.
func (s *Scheduler) run(ctx context.Context, j Job, prio Priority) error {
	start := time.Now().UTC()
	s.setStatus(j, func(st *JobStatus) {
		st.Running, st.State, st.StartedAt, st.LastRun = true, StateRunning, start, start
		st.Progress, st.Task = nil, ""
	})
	ctx = source.WithProgress(ctx, func(p source.Progress) {
		s.setStatus(j, func(st *JobStatus) {
			st.Task = p.Task
			if p.Percent < 0 {
				st.Progress = nil // phase without a percentage
			} else {
				v := p.Percent
				st.Progress = &v
			}
		})
	})
	msg, err := s.runOnce(ctx, j, prio)
	s.record(ctx, model.JobRun{Network: j.Network.Name, Kind: j.kind(), Source: j.Name(), Trigger: prio.trigger(), Message: msg}, start, err)
	s.setStatus(j, func(st *JobStatus) {
		st.Running, st.State, st.Priority = false, StateIdle, ""
		st.Progress, st.Task = nil, ""
		st.LastDuration = time.Since(start).Seconds()
		if err != nil {
			st.LastErr = err.Error()
		} else {
			st.LastErr = ""
			st.LastOK = time.Now().UTC()
		}
	})
	return err
}

// runOnce returns a summary of what was found.
func (s *Scheduler) runOnce(ctx context.Context, j Job, prio Priority) (string, error) {
	if j.Details != nil {
		return s.runDetails(ctx, j, prio)
	}
	if j.Ping != nil {
		return s.runPings(ctx, j.Network.Name, j.Ping)
	}
	obs, err := j.Source.Collect(ctx, j.Network)
	if err != nil {
		return "", err
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
		return "", err
	}
	last, err := s.st.LastSuccesses(ctx, name)
	if err != nil {
		return "", err
	}
	_, seenBefore := last[obs.Source]
	changed, events := diff.Apply(known, obs, !seenBefore)
	if err := s.st.UpsertDevices(ctx, changed); err != nil {
		return "", err
	}
	if err := s.st.ReplaceLinks(ctx, name, obs.Source, obs.Links); err != nil {
		return "", err
	}
	if err := s.st.SetLastSuccess(ctx, name, obs.Source, obs.At); err != nil {
		return "", err
	}
	last[obs.Source] = obs.At

	known, err = s.st.Devices(ctx, name)
	if err != nil {
		return "", err
	}
	off, offEvents := diff.Offline(known, name, last, s.thresholds(name))
	if err := s.st.UpsertDevices(ctx, off); err != nil {
		return "", err
	}
	events = append(events, offEvents...)
	if err := s.recordNmapRTTs(ctx, obs); err != nil {
		slog.Warn("storing ping times failed", "network", name, "err", err)
	}
	if err := s.st.AddEvents(ctx, events); err != nil {
		return "", err
	}
	if len(events) > 0 && s.sink != nil {
		s.sink.Enqueue(events)
	}
	if err := s.queueUnscanned(j); err != nil && ctx.Err() == nil {
		slog.Warn("queueing detailed scans failed", "network", name, "err", err)
	}
	msg := count(len(obs.Hosts), "host") + " seen"
	if len(events) > 0 {
		msg += ", " + count(len(events), "change")
	}
	return msg, nil
}

func humanDuration(d time.Duration) string { return config.FormatDuration(d) }
