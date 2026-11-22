package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/source"
)

// ErrNotRunning is returned by ScanDevice when the scheduler is not running.
var ErrNotRunning = errors.New("scheduler not running")

// runDetails scans the online devices of network (or only device onlyKey, online or not) and
// stores what was found. It never touches presence state and creates no events.
func (s *Scheduler) runDetails(ctx context.Context, network string, d DetailScanner, onlyKey string) error {
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return err
	}
	keys := map[netip.Addr]string{}
	var targets []netip.Addr
	for _, dev := range devs {
		if !dev.IP.IsValid() || (onlyKey == "" && !dev.Online) || (onlyKey != "" && dev.Key != onlyKey) {
			continue
		}
		if _, dup := keys[dev.IP]; !dup {
			targets = append(targets, dev.IP)
		}
		keys[dev.IP] = dev.Key
	}
	if len(targets) == 0 {
		return nil
	}
	hosts, err := d.Scan(ctx, targets)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var out []model.DeviceDetails
	for _, h := range hosts {
		if key, ok := keys[h.IP]; ok {
			out = append(out, model.DeviceDetails{Network: network, Key: key, ScannedAt: now, OS: h.OS,
				OSAccuracy: h.OSAccuracy, Ports: h.Ports})
		}
	}
	return s.st.SaveDetails(ctx, out)
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

// ScanDevice starts a detailed scan of one device in the background. It appears in Status
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
	var ip netip.Addr
	for _, dev := range devs {
		if dev.Key == key {
			ip = dev.IP
		}
	}
	if !ip.IsValid() {
		return fmt.Errorf("%w: %s in %s", ErrUnknownDevice, key, network)
	}
	id := network + "|" + key
	s.mu.Lock()
	if _, busy := s.adhoc[id]; busy {
		s.mu.Unlock()
		return nil // already scanning
	}
	st := &JobStatus{ID: -1, Network: network, Source: d.Name() + " " + ip.String(), Interval: "once", State: StateQueued}
	s.adhoc[id] = st
	s.mu.Unlock()

	s.adhocWG.Add(1)
	go func() {
		defer s.adhocWG.Done()
		defer func() { s.mu.Lock(); delete(s.adhoc, id); s.mu.Unlock() }()
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-s.sem }()
		start := time.Now().UTC()
		s.mu.Lock()
		st.State, st.Running, st.StartedAt = StateRunning, true, start
		s.mu.Unlock()
		pctx := source.WithProgress(ctx, func(p source.Progress) {
			s.mu.Lock()
			defer s.mu.Unlock()
			st.Task = p.Task
			if p.Percent >= 0 {
				v := p.Percent
				st.Progress = &v
			}
		})
		if err := s.runDetails(pctx, network, d, key); err != nil && ctx.Err() == nil {
			slog.Warn("device detail scan failed", "network", network, "device", key, "err", err)
		}
	}()
	return nil
}
