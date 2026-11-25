package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/diff"
	"github.com/gtrig/netmon/internal/enrich"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/presence"
	"github.com/gtrig/netmon/internal/source"
	"github.com/gtrig/netmon/pinger"
)

// Sweeper pings whole networks quickly.
type Sweeper interface {
	Name() string
	// Sweep pings every address count times and returns the times of those that answered.
	Sweep(ctx context.Context, addrs []netip.Addr, count int, gap time.Duration) (map[netip.Addr]time.Duration, error)
	// Reachable reports whether a host that does not answer pings is alive all the same.
	Reachable(ctx context.Context, addr netip.Addr) bool
	// Neighbors returns what the system knows of hosts on attached networks.
	Neighbors() map[netip.Addr]source.Neighbor
}

const (
	sweepPings   = 2
	sweepGap     = 200 * time.Millisecond
	confirmPings = 3
	confirmGap   = 300 * time.Millisecond
	confirmTCP   = 32              // hosts checked in parallel
	holdMax      = 5 * time.Minute // how long a broken path holds back offline marking
	holdMinHosts = 6               // losing more than half of at least this many is a broken path
	flushEvery   = time.Minute
)

// sweep is the state of one network's ping sweeps. Only its own loop changes it, except for
// rename, nmapSaw and the readers; mu guards everything below it.
type sweep struct {
	addrs []netip.Addr
	inNet map[netip.Addr]bool
	err   error // the network cannot be swept

	mu        sync.Mutex
	hosts     map[string]*hostState // by device key
	gaps      map[string][]time.Duration
	answering map[string]bool // who answered the last sweep that counted
	period    time.Duration   // real time between sweeps
	failed    bool            // the last sweep failed
	held      bool            // offline marking is held: the path looks broken
	heldSince time.Time
	flushed   time.Time
	rttAt     time.Time
}

type hostState struct {
	clock     time.Time // silent since
	seen      time.Time // last sign of life; zero until there was one
	misses    int
	discard   bool // the running gap is not the device's own doing
	offlineAt time.Time
}

type gap struct {
	key string
	d   time.Duration
}

func newSweep(j Job) *sweep {
	addrs, err := pinger.Hosts(j.Network.CIDR, j.Network.Exclude...)
	sw := &sweep{addrs: addrs, err: err, inNet: map[netip.Addr]bool{}, hosts: map[string]*hostState{}, period: j.Interval}
	for _, a := range addrs {
		sw.inNet[a] = true
	}
	return sw
}

// governs reports whether ping decides if d is offline: it has answered ping, and nothing has
// since shown that it blocks ping.
func (sw *sweep) governs(d model.Device, name string) bool {
	return sw != nil && sw.inNet[d.IP] && !d.SeenBy[name].IsZero()
}

// sight notes a sign of life. measure is whether it ends a gap.
func (sw *sweep) sight(key string, at time.Time, measure bool) (gap, bool) {
	h := sw.hosts[key]
	if h == nil {
		sw.hosts[key] = &hostState{clock: at, seen: at}
		return gap{}, false
	}
	d := at.Sub(h.clock)
	ok := measure && !h.discard && h.misses >= config.MissedScans && d <= presence.MaxGap
	h.clock, h.seen, h.misses, h.discard, h.offlineAt = at, at, 0, !measure, time.Time{}
	if ok {
		sw.gaps[key] = append(sw.gaps[key], d)
	}
	return gap{key, d}, ok
}

// grace restarts the clocks of everything silent.
func (sw *sweep) grace(at time.Time) {
	for _, h := range sw.hosts {
		h.discard = true
		if h.offlineAt.IsZero() {
			h.clock, h.misses = at, 0
		}
	}
}

func (sw *sweep) floor(n config.Network) time.Duration {
	return max(config.MissedScans*sw.period, n.OfflineAfter.Duration)
}

// hold decides whether the path looks broken, given who answered.
func (sw *sweep) hold(network string, now map[string]bool, at time.Time) {
	stay := 0
	for k := range sw.answering {
		if now[k] {
			stay++
		}
	}
	base := len(sw.answering)
	switch {
	case !sw.held:
		if (len(now) == 0 && base >= 2) || (base >= holdMinHosts && stay*2 < base) {
			sw.held, sw.heldSince = true, at
			slog.Warn("most devices stopped answering at once: holding offline marking", "network", network, "answering", stay, "before", base)
		}
	case stay*2 >= base:
		sw.held = false
		sw.grace(at)
	case at.Sub(sw.heldSince) >= holdMax:
		sw.held = false
	}
	if sw.held {
		for _, h := range sw.hosts {
			h.discard = h.discard || h.clock.Before(at)
		}
		return
	}
	sw.answering = now
}

// rename follows devices whose key changed.
func (sw *sweep) rename(changed []model.Device) {
	if sw == nil {
		return
	}
	sw.mu.Lock()
	defer sw.mu.Unlock()
	for _, d := range changed {
		from, to := d.PrevKey, d.Key
		if d.MergedInto != "" {
			from, to = d.Key, d.MergedInto
		}
		if from == "" || from == to {
			continue
		}
		if h := sw.hosts[from]; h != nil && (sw.hosts[to] == nil || sw.hosts[to].seen.Before(h.seen)) {
			sw.hosts[to] = h
		}
		delete(sw.hosts, from)
		if g := sw.gaps[from]; g != nil {
			sw.gaps[to] = append(g, sw.gaps[to]...)
			delete(sw.gaps, from)
		}
		if sw.answering[from] {
			delete(sw.answering, from)
			sw.answering[to] = true
		}
	}
}

// withoutStale drops the hosts ping knows to be offline from another source's hosts. Router and
// SNMP tables keep hosts that left; an nmap scan counts unless it began before the host went offline.
func (s *Scheduler) withoutStale(obs model.Observations, known []model.Device) []model.Host {
	sw := s.sweeps[obs.Network]
	if sw == nil {
		return obs.Hosts
	}
	sw.mu.Lock()
	defer sw.mu.Unlock()
	gone := func(h model.Host) bool {
		for _, d := range known {
			if d.Online || d.IP != h.IP || (h.MAC != "" && d.Key != model.DeviceKey(h)) || !sw.governs(d, s.sweepName(obs.Network)) {
				continue
			}
			st := sw.hosts[d.Key]
			if obs.Source != "nmap" || (st != nil && obs.At.Before(st.offlineAt)) {
				// Another online device at this address may be what was seen.
				return !slices.ContainsFunc(known, func(o model.Device) bool { return o.Online && o.IP == h.IP })
			}
		}
		return false
	}
	return slices.DeleteFunc(slices.Clone(obs.Hosts), gone)
}

// nmapSaw hands devices that nmap sees but that stay silent to pings back to the other sources'
// rules, until they answer a ping again. It changes SeenBy of changed.
func (s *Scheduler) nmapSaw(obs model.Observations, changed []model.Device) []gap {
	sw := s.sweeps[obs.Network]
	if sw == nil || obs.Source != "nmap" {
		return nil
	}
	name := s.sweepName(obs.Network)
	sw.mu.Lock()
	defer sw.mu.Unlock()
	var ended []gap
	for i := range changed {
		d := &changed[i]
		h := sw.hosts[d.Key]
		if d.MergedInto != "" || !sw.governs(*d, name) || h == nil || h.misses < config.MissedScans || !h.clock.Before(obs.At) {
			continue
		}
		if g, ok := sw.sight(d.Key, time.Now().UTC(), true); ok {
			ended = append(ended, g)
		}
		delete(d.SeenBy, name)
	}
	return ended
}

func (s *Scheduler) sweepName(network string) string {
	for _, j := range s.jobs {
		if j.Sweep != nil && j.Network.Name == network {
			return j.Sweep.Name()
		}
	}
	return ""
}

// storeGaps runs outside the sweep's lock.
func (s *Scheduler) storeGaps(ctx context.Context, network string, at time.Time, gs []gap) {
	for _, g := range gs {
		if err := s.st.AddGap(ctx, network, g.key, at, g.d); err != nil && ctx.Err() == nil {
			slog.Warn("storing a presence gap failed", "network", network, "device", g.key, "err", err)
		}
	}
}

// Presence returns how ping judges the devices it governs.
func (s *Scheduler) Presence(devs []model.Device) map[model.MemberRef]model.Presence {
	out := map[model.MemberRef]model.Presence{}
	for _, j := range s.jobs {
		if j.Sweep == nil {
			continue
		}
		sw := s.sweeps[j.Network.Name]
		sw.mu.Lock()
		floor := sw.floor(j.Network)
		for _, d := range devs {
			if d.Network != j.Network.Name || !sw.governs(d, j.Sweep.Name()) {
				continue
			}
			p := model.Presence{Pattern: presence.Learn(sw.gaps[d.Key])}
			p.Limit = presence.Limit(p.Pattern, d.OfflineAfter, floor)
			h := sw.hosts[d.Key]
			p.Asleep = d.Online && h != nil && !sw.held && h.misses >= config.MissedScans && p.Limit > floor
			out[model.MemberRef{Network: d.Network, Key: d.Key}] = p
		}
		sw.mu.Unlock()
	}
	return out
}

// SweepState is a snapshot of one network's ping sweeps, for debugging.
type SweepState struct {
	Network   string      `json:"network"`
	Addresses int         `json:"addresses"`
	Error     string      `json:"error,omitempty"`
	Period    string      `json:"period"`
	Failed    bool        `json:"lastSweepFailed"`
	Held      bool        `json:"offlineMarkingHeld"`
	HeldSince time.Time   `json:"heldSince,omitzero"`
	Answering int         `json:"answering"`
	Hosts     []SweepHost `json:"hosts"`
}

// SweepHost is what the sweeps know of one device.
type SweepHost struct {
	Key         string    `json:"key"`
	SilentSince time.Time `json:"silentSince"`
	Seen        time.Time `json:"lastSignOfLife,omitzero"`
	Misses      int       `json:"misses"`
	Answering   bool      `json:"answeredLastCountedSweep"`
	Discard     bool      `json:"gapDiscarded,omitempty"`
	OfflineAt   time.Time `json:"offlineAt,omitzero"`
	Gaps        []float64 `json:"gapSeconds,omitempty"`
}

// Sweeps returns the state of every network's ping sweeps.
func (s *Scheduler) Sweeps() []SweepState {
	out := []SweepState{}
	for _, j := range s.jobs {
		if j.Sweep == nil {
			continue
		}
		sw := s.sweeps[j.Network.Name]
		sw.mu.Lock()
		st := SweepState{Network: j.Network.Name, Addresses: len(sw.addrs), Period: sw.period.String(), Failed: sw.failed,
			Held: sw.held, HeldSince: sw.heldSince, Answering: len(sw.answering), Hosts: []SweepHost{}}
		if sw.err != nil {
			st.Error = sw.err.Error()
		}
		if !sw.held {
			st.HeldSince = time.Time{}
		}
		for _, k := range slices.Sorted(maps.Keys(sw.hosts)) {
			h := sw.hosts[k]
			sh := SweepHost{Key: k, SilentSince: h.clock, Seen: h.seen, Misses: h.misses, Answering: sw.answering[k],
				Discard: h.discard, OfflineAt: h.offlineAt}
			for _, g := range sw.gaps[k] {
				sh.Gaps = append(sh.Gaps, g.Seconds())
			}
			st.Hosts = append(st.Hosts, sh)
		}
		sw.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// runSweeps sweeps job i's network until ctx is done. It does not use the scan queue: a sweep is
// light and must not wait behind slow scans.
func (s *Scheduler) runSweeps(ctx context.Context, i int) {
	j := s.jobs[i]
	manual, failing := false, false
	for {
		start := time.Now().UTC()
		s.setStatusAt(i, func(st *JobStatus) {
			st.Running, st.State, st.StartedAt, st.LastRun = true, StateRunning, start, start
		})
		msg, err := s.sweepOnce(ctx, j)
		took := time.Since(start)
		slog.Debug("ping sweep", "network", j.Network.Name, "took", took.Round(time.Millisecond), "result", msg, "err", err)
		if ctx.Err() != nil {
			return
		}
		next := start.Add(max(j.Interval, took))
		s.setStatusAt(i, func(st *JobStatus) {
			st.Running, st.State, st.LastDuration, st.NextRun = false, StateIdle, took.Seconds(), next
			st.LastErr = ""
			if err != nil {
				st.LastErr = err.Error()
			} else {
				st.LastOK = time.Now().UTC()
			}
		})
		// A row per sweep would flood the history: only manual ones, failures and recoveries.
		if manual || (err != nil) != failing {
			prio := PrioPresence
			if manual {
				prio = PrioManual
			}
			s.record(ctx, model.JobRun{Network: j.Network.Name, Kind: j.kind(), Source: j.Name(), Trigger: prio.trigger(), Message: msg}, start, err)
		}
		if err != nil && !failing {
			slog.Warn("ping sweep failed", "network", j.Network.Name, "err", err)
		}
		failing, manual = err != nil, false
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-s.trigger[i]:
			t.Stop()
			manual = true
		}
	}
}

// sweepOnce sweeps the network once and applies what it found. It returns a summary.
func (s *Scheduler) sweepOnce(ctx context.Context, j Job) (string, error) {
	sw, name, network := s.sweeps[j.Network.Name], j.Sweep.Name(), j.Network.Name
	if sw.err != nil {
		return "", sw.err
	}
	start := time.Now()
	replies, err := j.Sweep.Sweep(ctx, sw.addrs, sweepPings, sweepGap)
	if err != nil {
		sw.mu.Lock()
		sw.failed = true
		sw.mu.Unlock()
		return "", err
	}
	at := time.Now().UTC()
	sw.mu.Lock()
	sw.period = max(j.Interval, time.Since(start))
	loaded := sw.gaps != nil
	sw.mu.Unlock()
	if !loaded {
		gaps, err := s.st.Gaps(ctx, network, at.Add(-presence.Window))
		if err != nil {
			return "", err
		}
		sw.mu.Lock()
		sw.gaps = gaps
		sw.mu.Unlock()
	}

	known, err := s.st.Devices(ctx, network)
	if err != nil {
		return "", err
	}
	hosts := identify(known, replies, j.Sweep.Neighbors())
	changes := 0
	var fresh []model.Host
	byKey := deviceIndex(known)
	for _, h := range hosts {
		d, ok := byKey[model.DeviceKey(h)]
		// A MAC-less row at this address is to be folded into the device.
		fold := h.MAC != "" && slices.ContainsFunc(known, func(o model.Device) bool { return o.MAC == "" && o.IP == h.IP })
		if !ok || !d.Online || d.IP != h.IP || d.SeenBy[name].IsZero() || fold {
			fresh = append(fresh, h)
		}
	}
	if len(fresh) > 0 {
		changes, err = s.applySweep(ctx, j, fresh, at)
		if err != nil {
			return "", err
		}
		if known, err = s.st.Devices(ctx, network); err != nil {
			return "", err
		}
	}

	now := map[string]bool{}
	for _, h := range hosts {
		now[model.DeviceKey(h)] = true
	}
	ended, suspects := sw.judge(j, known, now, at)
	s.storeGaps(ctx, network, at, ended)
	if len(suspects) > 0 {
		n, err := s.confirm(ctx, j, suspects, replies)
		if err != nil {
			return "", err
		}
		changes += n
	}
	if err := s.flushSweep(ctx, j, at); err != nil {
		return "", err
	}
	if err := s.sweepRTTs(ctx, j, replies, at); err != nil {
		slog.Warn("storing ping times failed", "network", network, "err", err)
	}
	msg := fmt.Sprintf("%d of %d addresses answered", len(replies), len(sw.addrs))
	if changes > 0 {
		msg += ", " + count(changes, "change")
	}
	return msg, nil
}

func deviceIndex(devs []model.Device) map[string]model.Device {
	out := map[string]model.Device{}
	for _, d := range devs {
		out[d.Key] = d
	}
	return out
}

// identify turns replies into hosts, at most one per device. A reply without a known MAC is the
// device most recently seen at that address.
func identify(known []model.Device, replies map[netip.Addr]time.Duration, near map[netip.Addr]source.Neighbor) []model.Host {
	at := map[netip.Addr]model.Device{}
	home := map[string]netip.Addr{} // MAC → the address its device has
	for _, d := range known {
		if d.MAC != "" {
			home[d.MAC] = d.IP
		}
		if best, ok := at[d.IP]; !ok || d.LastSeen.After(best.LastSeen) || (d.LastSeen.Equal(best.LastSeen) && best.MAC == "") {
			at[d.IP] = d
		}
	}
	var out []model.Host
	index := map[string]int{}
	addrs := make([]netip.Addr, 0, len(replies))
	for a := range replies {
		addrs = append(addrs, a)
	}
	slices.SortFunc(addrs, netip.Addr.Compare)
	for _, a := range addrs {
		h := model.Host{IP: a, MAC: near[a].MAC, RTT: replies[a]}
		if h.MAC == "" {
			h.MAC = at[a].MAC
		}
		// One device may answer at several addresses: it keeps the one it has.
		if i, ok := index[h.MAC]; ok && h.MAC != "" {
			if home[h.MAC] == a {
				out[i] = h
			}
			continue
		}
		if h.MAC != "" {
			index[h.MAC] = len(out)
		}
		out = append(out, h)
	}
	return out
}

func (s *Scheduler) applySweep(ctx context.Context, j Job, hosts []model.Host, at time.Time) (int, error) {
	network := j.Network.Name
	obs := model.Observations{Network: network, Source: j.Sweep.Name(), At: at, Hosts: hosts}
	enrich.Enrich(&obs, s.oui, s.res)
	lock := s.netLock[network]
	lock.Lock()
	defer lock.Unlock()
	known, err := s.st.Devices(ctx, network)
	if err != nil {
		return 0, err
	}
	last, err := s.st.LastSuccesses(ctx, network)
	if err != nil {
		return 0, err
	}
	_, seenBefore := last[obs.Source]
	changed, events := diff.Apply(known, obs, !seenBefore)
	if err := s.st.UpsertDevices(ctx, changed); err != nil {
		return 0, err
	}
	s.sweeps[network].rename(changed)
	if err := s.st.SetLastSuccess(ctx, network, obs.Source, at); err != nil {
		return 0, err
	}
	if err := s.emit(ctx, events); err != nil {
		return 0, err
	}
	if err := s.queueUnscanned(j); err != nil && ctx.Err() == nil {
		slog.Warn("queueing detailed scans failed", "network", network, "err", err)
	}
	return len(events), nil
}

func (s *Scheduler) emit(ctx context.Context, events []model.Event) error {
	if err := s.st.AddEvents(ctx, events); err != nil {
		return err
	}
	if len(events) > 0 && s.sink != nil {
		s.sink.Enqueue(events)
	}
	return nil
}

// judge notes who answered and returns the gaps that ended and the devices silent past their limit.
func (sw *sweep) judge(j Job, known []model.Device, now map[string]bool, at time.Time) (ended []gap, suspects []model.Device) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.failed {
		sw.failed = false
		sw.grace(at)
	}
	stored := deviceIndex(known)
	for k := range sw.hosts {
		if _, ok := stored[k]; !ok { // forgotten
			delete(sw.hosts, k)
			delete(sw.gaps, k)
		}
	}
	governed := map[string]bool{}
	for _, d := range known {
		if sw.governs(d, j.Sweep.Name()) && now[d.Key] {
			governed[d.Key] = true
		}
	}
	for k := range sw.answering {
		if d, ok := stored[k]; !ok || !sw.governs(d, j.Sweep.Name()) {
			delete(sw.answering, k)
		}
	}
	sw.hold(j.Network.Name, governed, at)
	for _, d := range known {
		if !sw.governs(d, j.Sweep.Name()) {
			if h := sw.hosts[d.Key]; h != nil {
				h.misses++
			}
			continue
		}
		if now[d.Key] {
			if g, ok := sw.sight(d.Key, at, true); ok {
				ended = append(ended, g)
			}
			continue
		}
		h := sw.hosts[d.Key]
		if h == nil { // netmon just started: the silence before does not count
			h = &hostState{clock: at, discard: true}
			sw.hosts[d.Key] = h
		}
		h.misses++
		if !d.Online {
			continue
		}
		if !h.offlineAt.IsZero() { // another source saw it at a new address
			h.clock, h.misses, h.discard, h.offlineAt = at, 1, true, time.Time{}
		}
		limit := presence.Limit(presence.Learn(sw.gaps[d.Key]), d.OfflineAfter, sw.floor(j.Network))
		if !sw.held && h.misses >= config.MissedScans && at.Sub(h.clock) >= limit {
			suspects = append(suspects, d)
		}
	}
	return ended, suspects
}

// confirm checks devices silent past their limit and marks those without any sign of life offline.
func (s *Scheduler) confirm(ctx context.Context, j Job, suspects []model.Device, replies map[netip.Addr]time.Duration) (int, error) {
	sw, network := s.sweeps[j.Network.Name], j.Network.Name
	var addrs []netip.Addr
	for _, d := range suspects {
		if _, taken := replies[d.IP]; !taken { // else another device answered there
			addrs = append(addrs, d.IP)
		}
	}
	answered := map[netip.Addr]time.Duration{}
	if len(addrs) > 0 {
		var err error
		if answered, err = j.Sweep.Sweep(ctx, addrs, confirmPings, confirmGap); err != nil {
			return 0, err
		}
	}
	near := j.Sweep.Neighbors()
	since := make([]time.Time, len(suspects)) // the last reply
	sw.mu.Lock()
	for i, d := range suspects {
		since[i] = sw.hosts[d.Key].clock
	}
	sw.mu.Unlock()
	alive := make([]bool, len(suspects))
	var wg sync.WaitGroup
	slots := make(chan struct{}, confirmTCP)
	for i, d := range suspects {
		if _, taken := replies[d.IP]; taken {
			continue
		}
		if _, ok := answered[d.IP]; ok {
			continue
		}
		// The system lists hosts long after they left: only a confirmation since the last reply counts.
		// A second of margin: the last reply is noted at the end of its sweep, cache times are coarse.
		if n := near[d.IP]; n.Confirmed.After(since[i].Add(time.Second)) && (d.MAC == "" || d.MAC == n.MAC) {
			alive[i] = true
			continue
		}
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			alive[i] = j.Sweep.Reachable(ctx, d.IP)
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	at := time.Now().UTC()
	var ended []gap
	lock := s.netLock[network]
	lock.Lock()
	defer lock.Unlock()
	known, err := s.st.Devices(ctx, network)
	if err != nil {
		return 0, err
	}
	stored := deviceIndex(known)
	var off []model.Device
	var events []model.Event
	sw.mu.Lock()
	for i, sus := range suspects {
		d, ok := stored[sus.Key]
		if !ok || !d.Online || d.IP != sus.IP || !sw.governs(d, j.Sweep.Name()) {
			continue // changed meanwhile
		}
		if _, ok := answered[d.IP]; ok || alive[i] {
			if g, rec := sw.sight(d.Key, at, ok); rec {
				ended = append(ended, g)
			}
			continue
		}
		d.Online = false
		if h := sw.hosts[d.Key]; h != nil && h.seen.After(d.LastSeen) {
			d.LastSeen = h.seen
		}
		off = append(off, d)
		events = append(events, model.Event{Type: model.EventDeviceOffline, Network: network, DeviceKey: d.Key, At: at, Message: "Offline: " + diff.Describe(d)})
	}
	sw.mu.Unlock()
	if err := s.st.UpsertDevices(ctx, off); err != nil {
		return 0, err
	}
	sw.mu.Lock()
	for _, d := range off {
		if h := sw.hosts[d.Key]; h != nil {
			h.offlineAt = at
		}
	}
	sw.mu.Unlock()
	s.storeGaps(ctx, network, at, ended)
	return len(events), s.emit(ctx, events)
}

// flushSweep stores when the governed devices were last seen, once a minute.
func (s *Scheduler) flushSweep(ctx context.Context, j Job, at time.Time) error {
	sw, name, network := s.sweeps[j.Network.Name], j.Sweep.Name(), j.Network.Name
	sw.mu.Lock()
	due := at.Sub(sw.flushed) >= flushEvery
	sw.mu.Unlock()
	if !due {
		return nil
	}
	lock := s.netLock[network]
	lock.Lock()
	defer lock.Unlock()
	known, err := s.st.Devices(ctx, network)
	if err != nil {
		return err
	}
	var changed []model.Device
	sw.mu.Lock()
	for _, d := range known {
		h := sw.hosts[d.Key]
		if h == nil || !sw.governs(d, name) || h.seen.Sub(d.SeenBy[name]) < time.Second {
			continue
		}
		d.SeenBy[name] = h.seen
		if h.seen.After(d.LastSeen) {
			d.LastSeen = h.seen
		}
		changed = append(changed, d)
	}
	sw.flushed = at
	sw.mu.Unlock()
	if len(changed) == 0 {
		return nil
	}
	if err := s.st.UpsertDevices(ctx, changed); err != nil {
		return err
	}
	return s.st.SetLastSuccess(ctx, network, name, at)
}

// sweepRTTs keeps ping times now and then, for networks no router pings.
func (s *Scheduler) sweepRTTs(ctx context.Context, j Job, replies map[netip.Addr]time.Duration, at time.Time) error {
	sw, network := s.sweeps[j.Network.Name], j.Network.Name
	sw.mu.Lock()
	due := at.Sub(sw.rttAt) >= PingInterval && !sw.held
	if due {
		sw.rttAt = at
	}
	sw.mu.Unlock()
	if !due || s.hasPingJob(network) {
		return nil
	}
	devs, err := s.st.Devices(ctx, network)
	if err != nil {
		return err
	}
	scope := map[netip.Addr]bool{}
	for _, d := range devs {
		scope[d.IP] = scope[d.IP] || sw.governs(d, j.Sweep.Name())
	}
	return s.recordRTTs(ctx, network, NmapOrigin, replies, func(a netip.Addr) bool { return scope[a] })
}
