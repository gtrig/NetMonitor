package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

// ErrUnknownChannel is returned for a channel name not in the configuration.
var ErrUnknownChannel = errors.New("unknown channel")

// FromConfig builds notifiers keyed by channel name.
func FromConfig(chs []config.Channel) (map[string]Notifier, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	out := map[string]Notifier{}
	for _, c := range chs {
		switch c.Type {
		case "telegram":
			out[c.Name] = NewTelegram(c, client, "")
		case "discord":
			out[c.Name] = NewDiscord(c, client)
		case "email":
			out[c.Name] = NewEmail(c, nil)
		default:
			return nil, fmt.Errorf("channel %q: unknown type %q", c.Name, c.Type)
		}
	}
	return out, nil
}

type job struct {
	channel string
	msg     Message
}

// Dispatcher routes events to channels by rules and delivers them asynchronously.
type Dispatcher struct {
	rules     []config.Rule
	notifiers map[string]Notifier
	known     func(key string) bool
	queue     chan job
	backoff   []time.Duration
	timeout   time.Duration
}

// NewDispatcher returns a dispatcher. known reports whether a device key is marked known (may be nil).
func NewDispatcher(rules []config.Rule, ns map[string]Notifier, known func(key string) bool) *Dispatcher {
	if known == nil {
		known = func(string) bool { return false }
	}
	return &Dispatcher{rules: rules, notifiers: ns, known: known, queue: make(chan job, 100),
		backoff: []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}, timeout: 10 * time.Second}
}

// Start runs the delivery worker until ctx is done.
func (d *Dispatcher) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-d.queue:
				d.deliver(ctx, j)
			}
		}
	}()
}

func (d *Dispatcher) deliver(ctx context.Context, j job) {
	n, ok := d.notifiers[j.channel]
	if !ok {
		return
	}
	for attempt := 0; ; attempt++ {
		sctx, cancel := context.WithTimeout(ctx, d.timeout)
		err := n.Send(sctx, j.msg)
		cancel()
		if err == nil {
			return
		}
		if attempt >= len(d.backoff) || ctx.Err() != nil {
			slog.Error("notify: giving up", "channel", j.channel, "err", err)
			return
		}
		slog.Warn("notify: send failed, retrying", "channel", j.channel, "err", err)
		select {
		case <-time.After(d.backoff[attempt]):
		case <-ctx.Done():
			return
		}
	}
}

// route returns channel → matching events (grouped by network).
func (d *Dispatcher) route(events []model.Event) map[string]map[string][]model.Event {
	out := map[string]map[string][]model.Event{}
	for _, e := range events {
		matched := map[string]bool{}
		for _, r := range d.rules {
			if len(r.Events) > 0 && !slices.Contains(r.Events, e.Type) {
				continue
			}
			if len(r.Networks) > 0 && !slices.Contains(r.Networks, e.Network) {
				continue
			}
			if r.OnlyUnknown && e.DeviceKey != "" && d.known(e.DeviceKey) {
				continue
			}
			for _, ch := range r.Channels {
				if matched[ch] {
					continue
				}
				matched[ch] = true
				if out[ch] == nil {
					out[ch] = map[string][]model.Event{}
				}
				out[ch][e.Network] = append(out[ch][e.Network], e)
			}
		}
	}
	return out
}

// Enqueue routes events and queues one message per channel and network. It never blocks;
// when the queue is full the message is dropped and logged.
func (d *Dispatcher) Enqueue(events []model.Event) {
	for ch, byNet := range d.route(events) {
		for network, evs := range byNet {
			m := Message{Title: fmt.Sprintf("netmon: %d change(s) on %s", len(evs), network)}
			for _, e := range evs {
				m.Lines = append(m.Lines, e.Message)
			}
			select {
			case d.queue <- job{channel: ch, msg: m}:
			default:
				slog.Error("notify: queue full, dropping message", "channel", ch, "network", network)
			}
		}
	}
}

// Test sends a test message to channel synchronously.
func (d *Dispatcher) Test(ctx context.Context, channel string) error {
	n, ok := d.notifiers[channel]
	if !ok {
		return ErrUnknownChannel
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return n.Send(ctx, Message{Title: "netmon test notification", Lines: []string{"If you can read this, channel " + channel + " works."}})
}
