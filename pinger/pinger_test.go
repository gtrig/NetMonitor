package pinger

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type packet struct {
	b    []byte
	from netip.Addr
}

type fakeConn struct {
	in    chan packet
	once  sync.Once
	gone  chan struct{}
	reply func(c *fakeConn, b []byte, to netip.Addr) error
	mu    sync.Mutex
	sends map[netip.Addr]int
}

func newFake(reply func(*fakeConn, []byte, netip.Addr) error) *fakeConn {
	return &fakeConn{in: make(chan packet, 1000), gone: make(chan struct{}), reply: reply, sends: map[netip.Addr]int{}}
}
func (c *fakeConn) count(a netip.Addr) int { c.mu.Lock(); defer c.mu.Unlock(); return c.sends[a] }
func (c *fakeConn) send(b []byte, to netip.Addr) error {
	c.mu.Lock()
	c.sends[to]++
	c.mu.Unlock()
	return c.reply(c, b, to)
}
func (c *fakeConn) recv(b []byte) (int, netip.Addr, error) {
	select {
	case p := <-c.in:
		return copy(b, p.b), p.from, nil
	case <-c.gone:
		return 0, netip.Addr{}, net.ErrClosed
	}
}
func (c *fakeConn) Close() error { c.once.Do(func() { close(c.gone) }); return nil }

func answer(b []byte) []byte { r := append([]byte(nil), b...); r[0] = echoReply; return r }

var a1, a2, a3 = netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3")

var fast = Options{Count: 2, Timeout: 30 * time.Millisecond, Gap: time.Millisecond, Rate: 100000}

func TestHosts(t *testing.T) {
	for cidr, want := range map[string]int{"10.0.0.7/24": 254, "10.0.0.0/30": 2, "10.0.0.0/31": 2, "10.0.0.1/32": 1, "10.0.0.0/16": 65534, "255.255.255.255/32": 1} {
		got, err := Hosts(netip.MustParsePrefix(cidr))
		if err != nil || len(got) != want {
			t.Errorf("%s: %d hosts, %v; want %d", cidr, len(got), err, want)
		}
	}
	if got, _ := Hosts(netip.MustParsePrefix("10.0.0.0/30"), a1); len(got) != 1 || got[0] != a2 {
		t.Errorf("exclude: %v", got)
	}
	for _, bad := range []string{"10.0.0.0/15", "fd00::/120"} {
		if _, err := Hosts(netip.MustParsePrefix(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestChecksum(t *testing.T) {
	if got := checksum(echo(probe{258, 1}, [8]byte{1, 2, 3, 4, 5, 6, 7, 8})); got != 0 {
		t.Errorf("checksum over message with checksum = %#x, want 0", got)
	}
}

func TestSweepCountsEveryHost(t *testing.T) {
	c := newFake(func(c *fakeConn, b []byte, to netip.Addr) error {
		if to == a1 || (to == a2 && c.count(a2) == 2) {
			c.in <- packet{answer(b), to}
			c.in <- packet{answer(b), to} // duplicate
		}
		return nil
	})
	o := fast
	o.Count = 4
	got, err := sweep(context.Background(), []conn{c}, []netip.Addr{a1, a2, a3, a1}, o)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	if g := got[a1]; g.Sent != 4 || g.Received != 4 {
		t.Errorf("a1: %+v", g)
	}
	if g := got[a2]; g.Sent != 4 || g.Received != 1 || g.Min != g.Max || g.Avg != g.Min {
		t.Errorf("a2: %+v", g)
	}
	for _, a := range []netip.Addr{a1, a2, a3} {
		if c.count(a) != 4 {
			t.Errorf("%s pinged %d times", a, c.count(a))
		}
	}
}

func TestTally(t *testing.T) {
	var y tally
	for _, ms := range []time.Duration{20, 10, 60} {
		y.add(ms * time.Millisecond)
	}
	if y.min != 10*time.Millisecond || y.max != 60*time.Millisecond || y.sum != 90*time.Millisecond || y.received != 3 {
		t.Errorf("%+v", y)
	}
}

func TestSweepGap(t *testing.T) {
	c := newFake(func(c *fakeConn, b []byte, to netip.Addr) error { c.in <- packet{answer(b), to}; return nil })
	start := time.Now()
	got, err := sweep(context.Background(), []conn{c}, []netip.Addr{a1}, Options{Count: 3, Gap: 50 * time.Millisecond})
	if d := time.Since(start); err != nil || got[a1].Received != 3 || d < 100*time.Millisecond || d > time.Second {
		t.Errorf("%v, %v after %v", got, err, d)
	}
}

func TestSweepCountLimit(t *testing.T) {
	if _, err := Sweep(context.Background(), []netip.Addr{a1}, Options{Count: MaxCount + 1}); err == nil {
		t.Error("accepted")
	}
}

func TestSweepIgnoresForeignReplies(t *testing.T) {
	c := newFake(func(c *fakeConn, b []byte, to netip.Addr) error {
		c.in <- packet{answer(b), a3} // wrong address
		c.in <- packet{b, to}         // our own request
		bad := answer(b)
		bad[8] ^= 1 // other token
		c.in <- packet{bad, to}
		future := answer(b)
		future[23] = 9 // attempt never sent
		c.in <- packet{future, to}
		c.in <- packet{answer(b)[:10], to}
		return nil
	})
	if got, err := sweep(context.Background(), []conn{c}, []netip.Addr{a1, a2}, fast); err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSweepLateReplyKeepsItsOwnTime(t *testing.T) {
	c := newFake(func(c *fakeConn, b []byte, to netip.Addr) error {
		if c.count(to) == 1 {
			r := answer(b)
			time.AfterFunc(60*time.Millisecond, func() { c.in <- packet{r, to} })
		}
		return nil
	})
	o := fast
	o.Gap, o.Timeout = 40*time.Millisecond, 100*time.Millisecond
	got, err := sweep(context.Background(), []conn{c}, []netip.Addr{a1}, o)
	if err != nil || got[a1].Min < 55*time.Millisecond {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSweepEndsEarlyWhenAllAnswer(t *testing.T) {
	c := newFake(func(c *fakeConn, b []byte, to netip.Addr) error { c.in <- packet{answer(b), to}; return nil })
	start := time.Now()
	got, err := sweep(context.Background(), []conn{c}, []netip.Addr{a1, a2}, Options{Timeout: 5 * time.Second})
	if err != nil || len(got) != 2 || time.Since(start) > 2*time.Second {
		t.Fatalf("got %v, %v after %v", got, err, time.Since(start))
	}
}

func TestSweepErrors(t *testing.T) {
	boom := errors.New("unreachable")
	dead := newFake(func(*fakeConn, []byte, netip.Addr) error { return boom })
	if _, err := sweep(context.Background(), []conn{dead}, []netip.Addr{a1, a2}, fast); !errors.Is(err, boom) {
		t.Errorf("all sends failing: %v", err)
	}
	one := newFake(func(c *fakeConn, b []byte, to netip.Addr) error {
		if to == a2 {
			return boom
		}
		c.in <- packet{answer(b), to}
		return nil
	})
	if got, err := sweep(context.Background(), []conn{one}, []netip.Addr{a1, a2}, fast); err != nil || len(got) != 1 {
		t.Errorf("one send failing: %v, %v", got, err)
	}
	lost := newFake(func(c *fakeConn, _ []byte, _ netip.Addr) error { c.Close(); return nil })
	if _, err := sweep(context.Background(), []conn{lost}, []netip.Addr{a1}, fast); err == nil {
		t.Error("socket failure not reported")
	}
}

func TestSweepCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	c := newFake(func(*fakeConn, []byte, netip.Addr) error { return nil })
	start := time.Now()
	if _, err := sweep(ctx, []conn{c}, []netip.Addr{a1}, Options{Timeout: 5 * time.Second}); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

func TestSweepPaces(t *testing.T) {
	c := newFake(func(*fakeConn, []byte, netip.Addr) error { return nil })
	addrs, _ := Hosts(netip.MustParsePrefix("10.0.0.0/27")) // 30
	start := time.Now()
	sweep(context.Background(), []conn{c}, addrs, Options{Count: 1, Timeout: time.Millisecond, Rate: 100})
	if d := time.Since(start); d < 280*time.Millisecond {
		t.Errorf("30 packets at 100/s took %v", d)
	}
}

func TestLoopback(t *testing.T) {
	lo := netip.MustParseAddr("127.0.0.1")
	c, err := listen()
	if err != nil {
		t.Skip(err)
	}
	got, err := sweep(context.Background(), []conn{c}, []netip.Addr{lo}, Options{})
	if err != nil || got[lo].Min <= 0 || got[lo].Received != DefaultCount {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSocketCount(t *testing.T) {
	o := Options{}.withDefaults()
	for n, want := range map[int]int{1: 1, 254: 6, 65534: 27} {
		if got := socketCount(n, o); got != want {
			t.Errorf("%d addresses: %d sockets, want %d", n, got, want)
		}
	}
	if got := socketCount(65534, Options{Rate: 1e6}.withDefaults()); got != maxSockets {
		t.Errorf("%d sockets, want the maximum", got)
	}
}

func TestSweepSpreadsOverSockets(t *testing.T) {
	up := func(c *fakeConn, b []byte, to netip.Addr) error { c.in <- packet{answer(b), to}; return nil }
	x, y := newFake(up), newFake(up)
	o := fast
	o.Count = 1
	got, err := sweep(context.Background(), []conn{x, y}, []netip.Addr{a1, a2, a3}, o)
	if err != nil || len(got) != 3 || len(x.sends) != 2 || len(y.sends) != 1 {
		t.Fatalf("got %v, %v, %v, %v", got, err, x.sends, y.sends)
	}
}
