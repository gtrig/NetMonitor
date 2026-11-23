package mikrotik

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
)

func TestParsePingTime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"1ms234us": 1234 * time.Microsecond, "12ms": 12 * time.Millisecond, "350us": 350 * time.Microsecond,
		"1s2ms": 1002 * time.Millisecond, "00:00:00.010": 10 * time.Millisecond, "0ms": 0,
	} {
		if got, ok := parsePingTime(in); !ok || got != want {
			t.Errorf("%q = %v, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "timeout", "-1ms"} {
		if got, ok := parsePingTime(in); ok {
			t.Errorf("%q = %v, want not ok", in, got)
		}
	}
}

// pingReplies fakes the router: address → reply times or an error.
type pingReplies struct {
	times map[string][]string
	errs  map[string]error
	asked []string
	args  []string
}

func (f *pingReplies) Run(ctx context.Context, path string, args ...string) ([]map[string]string, error) {
	ip := strings.TrimPrefix(args[0], "=address=")
	f.asked, f.args = append(f.asked, ip), append([]string{path}, args...)
	if err := f.errs[ip]; err != nil {
		return nil, err
	}
	var rows []map[string]string
	for _, v := range f.times[ip] {
		if v == "timeout" {
			rows = append(rows, map[string]string{"host": ip, "status": "timeout"})
		} else {
			rows = append(rows, map[string]string{"host": ip, "time": v})
		}
	}
	return rows, nil
}

var ipA, ipB = netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")

func pinger(f *pingReplies) *Pinger { return &Pinger{name: "gw", run: f} }

func TestPingMedian(t *testing.T) {
	f := &pingReplies{times: map[string][]string{"10.0.0.1": {"9ms", "1ms", "2ms"}, "10.0.0.2": {"timeout", "4ms", "timeout"}}}
	got, err := pinger(f).Ping(context.Background(), []netip.Addr{ipA, ipB})
	if err != nil || got[ipA] != 2*time.Millisecond || got[ipB] != 4*time.Millisecond {
		t.Fatalf("got %v, %v", got, err)
	}
	if strings.Join(f.args, " ") != "/ping =address=10.0.0.2 =count=3 =interval=200ms" {
		t.Fatalf("command = %v", f.args)
	}
	p := NewPinger(config.Router{Name: "gw"})
	if p.Name() != "ping:gw" || p.Origin() != "gw" {
		t.Fatalf("name %q origin %q", p.Name(), p.Origin())
	}
}

func TestPingTimeoutSkipsHost(t *testing.T) {
	f := &pingReplies{times: map[string][]string{"10.0.0.1": {"timeout", "timeout"}, "10.0.0.2": {"1ms"}}}
	got, err := pinger(f).Ping(context.Background(), []netip.Addr{ipA, ipB})
	if _, answered := got[ipA]; err != nil || answered || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPingDeniedStops(t *testing.T) {
	f := &pingReplies{errs: map[string]error{"10.0.0.1": errors.New("from RouterOS device: not enough permissions (9)")}}
	_, err := pinger(f).Ping(context.Background(), []netip.Addr{ipA, ipB})
	if !errors.Is(err, ErrPingDenied) || len(f.asked) != 1 {
		t.Fatalf("err %v, asked %v", err, f.asked)
	}
	if ErrPingDenied.Error() != `router user needs the "test" policy to ping` {
		t.Fatal(ErrPingDenied)
	}
}

func TestPingOneErrorKeepsOthers(t *testing.T) {
	f := &pingReplies{errs: map[string]error{"10.0.0.1": errors.New("boom")}, times: map[string][]string{"10.0.0.2": {"1ms"}}}
	got, err := pinger(f).Ping(context.Background(), []netip.Addr{ipA, ipB})
	if err != nil || got[ipB] != time.Millisecond {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPingAllErrors(t *testing.T) {
	f := &pingReplies{errs: map[string]error{"10.0.0.1": errors.New("boom"), "10.0.0.2": errors.New("bang")}}
	if _, err := pinger(f).Ping(context.Background(), []netip.Addr{ipA, ipB}); err == nil || !strings.Contains(err.Error(), "bang") {
		t.Fatalf("err = %v", err)
	}
}

func TestPingContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &pingReplies{}
	if _, err := pinger(f).Ping(ctx, []netip.Addr{ipA}); !errors.Is(err, context.Canceled) || len(f.asked) != 0 {
		t.Fatalf("err %v, asked %v", err, f.asked)
	}
}

func TestPingNoReplyRows(t *testing.T) {
	got, err := pinger(&pingReplies{}).Ping(context.Background(), []netip.Addr{ipA})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

// A router that stops answering must not make a run wait 30s for every remaining device.
func TestPingGivesUpOnDeadRouter(t *testing.T) {
	f := &pingReplies{errs: map[string]error{}}
	var addrs []netip.Addr
	for i := range 20 {
		a := netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)})
		addrs = append(addrs, a)
		f.errs[a.String()] = errors.New("i/o timeout")
	}
	if _, err := pinger(f).Ping(context.Background(), addrs); err == nil || len(f.asked) != 3 {
		t.Fatalf("err %v after %d requests", err, len(f.asked))
	}
}

type rowRunner []map[string]string

func (r rowRunner) Run(context.Context, string, ...string) ([]map[string]string, error) {
	return r, nil
}

func TestPingErrorRepliesAreNotAnswers(t *testing.T) {
	p := &Pinger{name: "gw", run: rowRunner{{"host": "10.0.0.254", "time": "986ms", "status": "host unreachable"}}}
	if got, err := p.Ping(context.Background(), []netip.Addr{ipA}); err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}
