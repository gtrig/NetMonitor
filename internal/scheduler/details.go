package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/source"
)

// ErrNotRunning is returned by ScanDevice when the scheduler is not running.
var ErrNotRunning = errors.New("scheduler not running")

// Automatic detailed scans of a device that failed are retried after retryMin, doubling up to
// retryMax. Cancelling one postpones the next by retryMax.
const (
	retryMin = time.Hour
	retryMax = 24 * time.Hour
)

type retry struct {
	at    time.Time
	delay time.Duration
}

// deviceScan is a queued or running detailed scan of one device. skipped and err are set before
// done is closed.
type deviceScan struct {
	status  *JobStatus // guarded by Scheduler.mu
	done    chan struct{}
	key     string // where the result was stored
	message string // what was found
	skipped bool   // cancelled, or nothing to scan any more
	err     error
}

func deviceID(network, key string) string { return "device:" + network + "|" + key }

// detailable reports whether dev can be part of unattended detailed scans.
func detailable(dev model.Device) bool { return dev.Online && dev.IP.Is4() }

// runDetails queues a detailed scan of every online device of network and waits for them all.
// It never touches presence state and creates no events. Scheduled runs skip devices scanned
// within half an interval, so a run cut short by a restart is resumed.
func (s *Scheduler) runDetails(ctx context.Context, j Job, prio Priority) (string, error) {
	network := j.Network.Name
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return "", err
	}
	stored, err := s.st.Details(ctx, network)
	if err != nil {
		return "", err
	}
	var scans []*deviceScan
	for _, dev := range devs {
		if !detailable(dev) {
			continue
		}
		at := stored[model.MemberRef{Network: network, Key: dev.Key}].ScannedAt
		if prio != PrioManual && time.Since(at) < j.Interval/2 {
			continue
		}
		ds, err := s.startDevice(ctx, j.Details, network, dev.Key, dev.IP, prio)
		if err != nil {
			return "", err
		}
		scans = append(scans, ds)
	}
	var failed, ok int
	var firstErr error
	for i, ds := range scans {
		source.ReportProgress(ctx, source.Progress{Task: fmt.Sprintf("%d of %d devices", i, len(scans)),
			Percent: 100 * float64(i) / float64(len(scans))})
		select {
		case <-ds.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		switch {
		case ds.err != nil:
			failed++
			if firstErr == nil {
				firstErr = ds.err
			}
		case !ds.skipped:
			ok++
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if failed == 0 || ok > 0 {
		// Recorded so a restart waits for the interval instead of rescanning. Offline detection
		// ignores this entry (no device is seen by it).
		if err := s.st.SetLastSuccess(ctx, network, j.Details.Name(), time.Now().UTC()); err != nil {
			return "", err
		}
	}
	if failed > 0 {
		return "", fmt.Errorf("%d of %d device scans failed: %w", failed, len(scans), firstErr)
	}
	if len(scans) == 0 {
		return "no devices to scan", nil
	}
	msg := count(ok, "device") + " scanned"
	if n := len(scans) - ok; n > 0 {
		msg += fmt.Sprintf(", %d skipped", n)
	}
	return msg, nil
}

// firstDelay is how long a details job waits after start: the rest of its interval since its
// last recorded run, or 0.
func (s *Scheduler) firstDelay(ctx context.Context, j Job) time.Duration {
	if j.Details == nil {
		return 0
	}
	last, err := s.st.LastSuccesses(ctx, j.Network.Name)
	if err != nil {
		return 0
	}
	t, ok := last[j.Details.Name()]
	if !ok {
		return 0
	}
	if wait := time.Until(t.Add(j.Interval)); wait > 0 {
		return wait
	}
	return 0
}

// scannerFor returns the network's services scanner, or DefaultDetails.
func (s *Scheduler) scannerFor(network string) DetailScanner {
	for _, j := range s.jobs {
		if j.Network.Name == network && j.Details != nil {
			return j.Details
		}
	}
	return s.DefaultDetails
}

// queueUnscanned queues a detailed scan of every online device of the network that has none.
func (s *Scheduler) queueUnscanned(j Job) error {
	s.mu.Lock()
	ctx := s.runCtx
	s.mu.Unlock()
	network := j.Network.Name
	d := s.scannerFor(network)
	if ctx == nil || ctx.Err() != nil || d == nil || !j.Network.AutoDetailsOn() {
		return nil
	}
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return err
	}
	stored, err := s.st.Details(ctx, network)
	if err != nil {
		return err
	}
	for _, dev := range devs {
		if _, done := stored[model.MemberRef{Network: network, Key: dev.Key}]; done || !detailable(dev) {
			continue
		}
		s.mu.Lock()
		wait := time.Now().Before(s.retries[deviceID(network, dev.Key)].at)
		s.mu.Unlock()
		if wait {
			continue
		}
		if _, err := s.startDevice(ctx, d, network, dev.Key, dev.IP, PrioAuto); err != nil {
			return err
		}
	}
	return nil
}

// ScanDevice queues a detailed scan of one device ahead of other scans. It appears in Status
// (with ID -1) until it finishes.
func (s *Scheduler) ScanDevice(network, key string) error {
	s.mu.Lock()
	ctx := s.runCtx
	s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return ErrNotRunning
	}
	d := s.scannerFor(network)
	if d == nil {
		return errors.New("no detailed scanner configured")
	}
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(devs, func(dev model.Device) bool { return dev.Key == key })
	if i < 0 || !devs[i].IP.IsValid() {
		return fmt.Errorf("%w: %s in %s", ErrUnknownDevice, key, network)
	}
	_, err = s.startDevice(ctx, d, network, key, devs[i].IP, PrioManual)
	return err
}

// startDevice queues a detailed scan of one device, or raises the priority of the one it has.
func (s *Scheduler) startDevice(ctx context.Context, d DetailScanner, network, key string, ip netip.Addr, prio Priority) (*deviceScan, error) {
	id := deviceID(network, key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || ctx.Err() != nil {
		return nil, ErrNotRunning
	}
	if ds := s.devices[id]; ds != nil {
		s.queue.promote(id, prio)
		return ds, nil
	}
	ds := &deviceScan{done: make(chan struct{}), status: &JobStatus{ID: -1, QueueID: id, Network: network,
		Source: d.Name() + " " + ip.String(), Interval: "once", State: StateQueued, Priority: prio.String()}}
	s.devices[id] = ds
	e := s.queue.enter(id, true, prio)
	s.deviceWG.Add(1) // under mu, so Run's Wait cannot have started
	go func() {
		defer s.deviceWG.Done()
		ds.skipped, ds.err = s.runDevice(ctx, d, network, key, ds, e)
		s.recordDevice(ctx, network, key, ds, e)
		s.mu.Lock()
		delete(s.devices, id)
		switch {
		case ctx.Err() != nil:
		case errors.Is(ds.err, ErrCancelled):
			ds.skipped, ds.err = true, nil
			s.retries[id] = retry{at: time.Now().Add(retryMax), delay: retryMax}
		case ds.err != nil:
			delay := min(max(2*s.retries[id].delay, retryMin), retryMax)
			s.retries[id] = retry{at: time.Now().Add(delay), delay: delay}
			slog.Warn("device detail scan failed", "network", network, "device", key, "err", ds.err)
		case !ds.skipped:
			delete(s.retries, id)
		}
		s.mu.Unlock()
		close(ds.done)
	}()
	return ds, nil
}

// runDevice waits for a worker, scans the device and stores the result.
func (s *Scheduler) runDevice(ctx context.Context, d DetailScanner, network, key string, ds *deviceScan, e *entry) (skipped bool, err error) {
	if err := s.queue.wait(ctx, e); err != nil {
		return false, err
	}
	defer s.queue.release(e)
	prio := s.queue.priority(e)
	// The device may have changed while the scan was queued.
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(devs, func(dev model.Device) bool { return dev.Key == key })
	if i < 0 || !devs[i].IP.IsValid() || (prio != PrioManual && !detailable(devs[i])) {
		return true, nil
	}
	ip := devs[i].IP
	s.mu.Lock()
	ds.status.State, ds.status.Running, ds.status.StartedAt = StateRunning, true, time.Now().UTC()
	ds.status.Source, ds.status.Priority = d.Name()+" "+ip.String(), prio.String()
	s.mu.Unlock()
	pctx := source.WithProgress(ctx, func(p source.Progress) {
		s.mu.Lock()
		defer s.mu.Unlock()
		ds.status.Task, ds.status.Progress = p.Task, nil
		if p.Percent >= 0 {
			v := p.Percent
			ds.status.Progress = &v
		}
	})
	hosts, err := d.Scan(pctx, []netip.Addr{ip})
	if err != nil {
		return false, err
	}
	i = slices.IndexFunc(hosts, func(h model.HostDetails) bool { return h.IP == ip })
	if i < 0 {
		return false, fmt.Errorf("%s: no result", ip)
	}
	h := hosts[i]
	ds.message = deviceSummary(h)
	ds.key = s.keyNow(ctx, network, key, ip)
	return false, s.st.SaveDetails(ctx, []model.DeviceDetails{{Network: network, Key: ds.key,
		ScannedAt: time.Now().UTC(), OS: h.OS, OSAccuracy: h.OSAccuracy, Ports: h.Ports}})
}

func (s *Scheduler) recordDevice(ctx context.Context, network, key string, ds *deviceScan, e *entry) {
	s.mu.Lock()
	src, start := ds.status.Source, ds.status.StartedAt
	s.mu.Unlock()
	if ds.key != "" {
		key = ds.key
	}
	r := model.JobRun{Network: network, Kind: model.RunDevice, Source: src, Key: key,
		Trigger: s.queue.priority(e).trigger(), Message: ds.message}
	if ds.skipped {
		r.Outcome, r.Message = model.OutcomeSkipped, "the device is offline or no longer listed"
	}
	s.record(ctx, r, start, ds.err)
}

// keyNow returns key, or the key its device got while it was being scanned (e.g. once its MAC
// address became known).
func (s *Scheduler) keyNow(ctx context.Context, network, key string, ip netip.Addr) string {
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return key
	}
	now := key
	for _, dev := range devs {
		if dev.Key == key {
			return key
		}
		if dev.IP == ip && dev.Online {
			now = dev.Key
		}
	}
	return now
}

// Cancel drops a waiting scan by its queue id. Running scans are not stopped.
func (s *Scheduler) Cancel(queueID string) error {
	if !s.queue.cancel(queueID) {
		return ErrNotQueued
	}
	return nil
}
