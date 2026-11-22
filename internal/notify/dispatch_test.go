package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

type recorder struct {
	mu    sync.Mutex
	msgs  []Message
	fails int
	block chan struct{}
	calls int
}

func (r *recorder) Name() string { return "rec" }
func (r *recorder) Send(ctx context.Context, m Message) error {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fails > 0 {
		r.fails--
		return errors.New("temporary")
	}
	r.msgs = append(r.msgs, m)
	return nil
}
func (r *recorder) snapshot() ([]Message, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.msgs...), r.calls
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestRulesOnlyUnknown(t *testing.T) {
	rec := &recorder{}
	rules := []config.Rule{{Events: []string{model.EventDeviceNew}, OnlyUnknown: true, Channels: []string{"rec"}}}
	d := NewDispatcher(rules, map[string]Notifier{"rec": rec}, func(key string) bool { return key == "mac:known" })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	d.Enqueue([]model.Event{
		{Type: model.EventDeviceNew, Network: "lan", DeviceKey: "mac:known", Message: "known one"},
		{Type: model.EventDeviceNew, Network: "lan", DeviceKey: "mac:new", Message: "new one"},
		{Type: model.EventDeviceOffline, Network: "lan", DeviceKey: "mac:new", Message: "offline"},
	})
	waitFor(t, func() bool { m, _ := rec.snapshot(); return len(m) == 1 })
	m, _ := rec.snapshot()
	if len(m[0].Lines) != 1 || m[0].Lines[0] != "new one" || m[0].Title != "netmon: 1 change(s) on lan" {
		t.Fatalf("msg = %+v", m[0])
	}
}

func TestRulesNetworkFilter(t *testing.T) {
	rec := &recorder{}
	rules := []config.Rule{{Networks: []string{"iot"}, Channels: []string{"rec"}}}
	d := NewDispatcher(rules, map[string]Notifier{"rec": rec}, nil)
	if got := d.route([]model.Event{{Type: model.EventDeviceNew, Network: "lan"}}); len(got) != 0 {
		t.Fatalf("routed = %+v", got)
	}
}

func TestDispatchDoesNotBlock(t *testing.T) {
	rec := &recorder{block: make(chan struct{})}
	defer close(rec.block)
	d := NewDispatcher([]config.Rule{{Channels: []string{"rec"}}}, map[string]Notifier{"rec": rec}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	start := time.Now()
	for i := 0; i < 200; i++ {
		d.Enqueue([]model.Event{{Type: model.EventDeviceNew, Network: "lan", Message: "x"}})
	}
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Fatalf("Enqueue blocked for %v", el)
	}
}

func TestDispatchRetries(t *testing.T) {
	rec := &recorder{fails: 2}
	d := NewDispatcher([]config.Rule{{Channels: []string{"rec"}}}, map[string]Notifier{"rec": rec}, nil)
	d.backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	d.Enqueue([]model.Event{{Type: model.EventDeviceNew, Network: "lan", Message: "x"}})
	waitFor(t, func() bool { m, _ := rec.snapshot(); return len(m) == 1 })
	if _, calls := rec.snapshot(); calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestTestUnknownChannel(t *testing.T) {
	d := NewDispatcher(nil, map[string]Notifier{}, nil)
	if err := d.Test(context.Background(), "nope"); !errors.Is(err, ErrUnknownChannel) {
		t.Fatalf("err = %v", err)
	}
}

// Finding 7: a stuck channel must not delay other channels.
func TestChannelsDeliveredIndependently(t *testing.T) {
	stuck := &recorder{block: make(chan struct{})}
	defer close(stuck.block)
	ok := &recorder{}
	d := NewDispatcher([]config.Rule{{Channels: []string{"stuck", "ok"}}}, map[string]Notifier{"stuck": stuck, "ok": ok}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	for i := 0; i < 3; i++ {
		d.Enqueue([]model.Event{{Type: model.EventDeviceNew, Network: "lan", Message: "x"}})
	}
	waitFor(t, func() bool { m, _ := ok.snapshot(); return len(m) == 3 })
}
