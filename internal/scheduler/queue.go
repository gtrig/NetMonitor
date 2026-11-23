package scheduler

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
)

// ErrCancelled is returned to a waiting scan that was cancelled.
var ErrCancelled = errors.New("cancelled")

// ErrNotQueued is returned by Cancel for a scan that is not waiting.
var ErrNotQueued = errors.New("not queued")

// Priority orders waiting scans; lower runs first.
type Priority int

const (
	PrioManual    Priority = iota
	PrioPresence           // scheduled router/snmp/nmap collections
	PrioScheduled          // devices of a scheduled detailed scan
	PrioAuto               // devices that were never scanned in detail
)

func (p Priority) String() string {
	return [...]string{"manual", "presence", "scheduled", "automatic"}[p]
}

// queue hands out worker slots by priority, first come first served within one.
type queue struct {
	mu              sync.Mutex
	scans           int // slots
	detailed        int // how many of them detailed scans may use
	running         int
	runningDetailed int
	seq             int
	waiting         []*entry // in queue order
}

// entry is one scan waiting for, or holding, a slot. Fields are guarded by queue.mu.
type entry struct {
	id       string
	detailed bool
	prio     Priority
	seq      int
	granted  bool
	err      error
	ready    chan struct{} // closed when granted or cancelled
}

func (q *queue) enter(id string, detailed bool, prio Priority) *entry {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	e := &entry{id: id, detailed: detailed, prio: prio, seq: q.seq, ready: make(chan struct{})}
	q.waiting = append(q.waiting, e)
	q.sort()
	q.dispatch()
	return e
}

func (q *queue) sort() {
	slices.SortFunc(q.waiting, func(a, b *entry) int {
		return cmp.Or(cmp.Compare(a.prio, b.prio), cmp.Compare(a.seq, b.seq))
	})
}

// dispatch grants free slots to the first entries allowed to use them.
func (q *queue) dispatch() {
	for q.running < q.scans {
		i := slices.IndexFunc(q.waiting, func(e *entry) bool { return !e.detailed || q.runningDetailed < q.detailed })
		if i < 0 {
			return
		}
		e := q.waiting[i]
		q.waiting = slices.Delete(q.waiting, i, i+1)
		q.running++
		if e.detailed {
			q.runningDetailed++
		}
		e.granted = true
		close(e.ready)
	}
}

// wait blocks until e has a slot (which must be released) or returns why it will not get one.
func (q *queue) wait(ctx context.Context, e *entry) error {
	select {
	case <-e.ready:
	case <-ctx.Done():
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	if err := ctx.Err(); err != nil {
		if e.granted {
			q.free(e)
		} else {
			q.remove(e)
		}
		return err
	}
	return nil
}

func (q *queue) release(e *entry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.free(e)
}

func (q *queue) free(e *entry) {
	q.running--
	if e.detailed {
		q.runningDetailed--
	}
	q.dispatch()
}

func (q *queue) remove(e *entry) {
	if i := slices.Index(q.waiting, e); i >= 0 {
		q.waiting = slices.Delete(q.waiting, i, i+1)
	}
}

func (q *queue) find(id string) *entry {
	for _, e := range q.waiting {
		if e.id == id {
			return e
		}
	}
	return nil
}

// promote raises a waiting entry to prio and reports whether id was waiting.
func (q *queue) promote(id string, prio Priority) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.find(id)
	if e == nil {
		return false
	}
	if prio < e.prio {
		e.prio = prio
		q.sort()
	}
	return true
}

// cancel removes a waiting entry and reports whether id was waiting.
func (q *queue) cancel(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.find(id)
	if e == nil {
		return false
	}
	q.remove(e)
	e.err = ErrCancelled
	close(e.ready)
	return true
}

func (q *queue) priority(e *entry) Priority {
	q.mu.Lock()
	defer q.mu.Unlock()
	return e.prio
}

type place struct {
	pos  int // 1 = first
	prio Priority
}

// places returns where each waiting entry stands.
func (q *queue) places() map[string]place {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := map[string]place{}
	for i, e := range q.waiting {
		out[e.id] = place{i + 1, e.prio}
	}
	return out
}
