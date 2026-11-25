// Package pinger finds active IPv4 hosts with concurrent ICMP echo sweeps.
// It uses only the standard library and nothing else from netmon.
package pinger

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"time"
)

// Defaults for zero Options fields.
const (
	DefaultCount   = 3
	DefaultTimeout = time.Second
	DefaultGap     = 200 * time.Millisecond
	DefaultRate    = 1000
	// MaxCount is the most pings per host.
	MaxCount = 1000
	// MinBits is the largest network (shortest prefix) Hosts expands.
	MinBits = 16
)

// Options tunes a sweep; zero fields use the defaults.
type Options struct {
	Count   int           // pings per host
	Timeout time.Duration // how long to wait for replies after the last ping
	Gap     time.Duration // minimum time between two pings of the same host
	Rate    int           // packets per second
}

// Stats sums up the replies of one active host.
type Stats struct {
	Sent, Received int
	Min, Avg, Max  time.Duration
}

// Hosts returns the host addresses of an IPv4 prefix, without its network and broadcast addresses.
func Hosts(p netip.Prefix, exclude ...netip.Addr) ([]netip.Addr, error) {
	if !p.IsValid() || !p.Addr().Is4() {
		return nil, fmt.Errorf("%s: not an IPv4 network", p)
	}
	if p.Bits() < MinBits {
		return nil, fmt.Errorf("%s: larger than /%d", p, MinBits)
	}
	p = p.Masked()
	var out []netip.Addr
	for a := p.Addr(); p.Contains(a); a = a.Next() {
		out = append(out, a)
	}
	if p.Bits() < 31 {
		out = out[1 : len(out)-1]
	}
	return slices.DeleteFunc(out, func(a netip.Addr) bool { return slices.Contains(exclude, a) }), nil
}

// Sweep pings every address Count times and returns the response times of those that answered
// at least once. On error (including a cancelled ctx) it also returns what had answered so far;
// that set is incomplete and proves nothing about the other addresses.
func Sweep(ctx context.Context, addrs []netip.Addr, o Options) (map[netip.Addr]Stats, error) {
	if o.Count > MaxCount {
		return nil, fmt.Errorf("count %d: at most %d", o.Count, MaxCount)
	}
	for _, a := range addrs {
		if !a.Is4() {
			return nil, fmt.Errorf("%s: not an IPv4 address", a)
		}
	}
	if len(addrs) == 0 {
		return map[netip.Addr]Stats{}, ctx.Err()
	}
	o = o.withDefaults()
	var conns []conn
	for range socketCount(len(addrs), o) {
		c, err := listen()
		if err != nil {
			if len(conns) > 0 {
				break // e.g. out of file descriptors: fewer sockets only make it slower
			}
			return nil, err
		}
		conns = append(conns, c)
	}
	return sweep(ctx, conns, addrs, o)
}

func (o Options) withDefaults() Options {
	if o.Count <= 0 {
		o.Count = DefaultCount
	}
	if o.Gap <= 0 {
		o.Gap = DefaultGap
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Rate <= 0 {
		o.Rate = DefaultRate
	}
	return o
}

const (
	arpHold          = 4 // seconds the kernel may hold a packet to a silent local address
	packetsPerSocket = 150
	maxSockets       = 64
)

// socketCount is how many sockets keep sends from blocking. Packets waiting for ARP fill the
// send buffer of their socket: the Linux default takes about 250 of them.
func socketCount(addrs int, o Options) int {
	waiting := min(addrs*o.Count, o.Rate*arpHold)
	return min(maxSockets, max(1, (waiting+packetsPerSocket-1)/packetsPerSocket))
}

// conn sends and receives ICMP messages (without IP header).
type conn interface {
	send(b []byte, to netip.Addr) error
	recv(b []byte) (int, netip.Addr, error)
	Close() error
}

type probe struct {
	index int // into the addresses
	round int
}

type tally struct {
	sent, received int
	min, max, sum  time.Duration
}

// sweep owns and closes conns.
func sweep(ctx context.Context, conns []conn, addrs []netip.Addr, o Options) (map[netip.Addr]Stats, error) {
	o = o.withDefaults()
	addrs = slices.Clone(addrs)
	slices.SortFunc(addrs, netip.Addr.Compare)
	addrs = slices.Compact(addrs)

	var token [8]byte
	rand.Read(token[:])
	var (
		mu      sync.Mutex
		pending = map[probe]time.Time{} // sent and not answered
		tallies = make([]tally, len(addrs))
		recvErr error
		closing bool
		allSent bool
	)
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	allUp := make(chan struct{}, 1) // everything sent was answered
	var receivers sync.WaitGroup
	receive := func(c conn) {
		buf := make([]byte, 1500)
		for {
			n, from, err := c.recv(buf)
			now := time.Now()
			mu.Lock()
			if err != nil {
				if !closing && recvErr == nil {
					recvErr = fmt.Errorf("receive: %w", err)
					stop(recvErr)
				}
				mu.Unlock()
				return
			}
			if p, ok := parseReply(buf[:n], token); ok && p.index < len(addrs) && addrs[p.index] == from {
				if at, asked := pending[p]; asked { // else a duplicate, or never sent
					delete(pending, p)
					tallies[p.index].add(now.Sub(at))
					if allSent && len(pending) == 0 {
						allUp <- struct{}{} // once: nothing is added after allSent
					}
				}
			}
			mu.Unlock()
		}
	}
	for _, c := range conns {
		receivers.Go(func() { receive(c) })
	}

	send := func(p probe) error {
		c, b := conns[p.index%len(conns)], echo(p, token)
		var err error
		for range 6 {
			mu.Lock()
			pending[p] = time.Now()
			mu.Unlock()
			if err = c.send(b, addrs[p.index]); !errors.Is(err, syscall.ENOBUFS) {
				break
			}
			if sleep(ctx, 2*time.Millisecond, nil) != nil {
				break
			}
		}
		mu.Lock()
		defer mu.Unlock()
		tallies[p.index].sent++
		if err != nil {
			delete(pending, p)
		}
		return err
	}

	err := func() error {
		sentAny := false
		var lastErr error
		spacing := time.Second / time.Duration(o.Rate)
		next := time.Now()
		for round := range o.Count {
			start := time.Now()
			if start.After(next) {
				next = start
			}
			for i := range addrs {
				// Paced against a schedule, so timer granularity does not lower the rate.
				if wait := time.Until(next); wait > 0 {
					if err := sleep(ctx, wait, nil); err != nil {
						return err
					}
				} else if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				next = next.Add(spacing)
				if err := send(probe{i, round}); err != nil {
					lastErr = err // e.g. no route to this one address
				} else {
					sentAny = true
				}
			}
			if !sentAny {
				return fmt.Errorf("send: %w", lastErr)
			}
			if round < o.Count-1 {
				if err := sleep(ctx, time.Until(start.Add(o.Gap)), nil); err != nil {
					return err
				}
			}
		}
		mu.Lock()
		allSent = true
		answered := len(pending) == 0
		mu.Unlock()
		if answered {
			return nil
		}
		return sleep(ctx, o.Timeout, allUp)
	}()
	mu.Lock()
	closing = true
	mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
	receivers.Wait()
	if err == nil {
		err = recvErr
	}
	out := map[netip.Addr]Stats{}
	for i, t := range tallies {
		if t.received > 0 {
			out[addrs[i]] = Stats{Sent: t.sent, Received: t.received, Min: t.min, Avg: t.sum / time.Duration(t.received), Max: t.max}
		}
	}
	return out, err
}

func (t *tally) add(rtt time.Duration) {
	if t.received == 0 || rtt < t.min {
		t.min = rtt
	}
	t.max = max(t.max, rtt)
	t.sum += rtt
	t.received++
}

// sleep waits for d, or until early is closed or ctx ends (an error).
func sleep(ctx context.Context, d time.Duration, early <-chan struct{}) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-early:
	case <-t.C:
	}
	return nil
}

const (
	echoRequest = 8
	echoReply   = 0
	echoLen     = 8 + 8 + 4 + 4 // header, token, index, round
)

func echo(p probe, token [8]byte) []byte {
	b := make([]byte, echoLen)
	b[0] = echoRequest
	copy(b[4:6], token[:2]) // id; the kernel replaces it on unprivileged sockets
	binary.BigEndian.PutUint16(b[6:], uint16(p.index))
	copy(b[8:], token[:])
	binary.BigEndian.PutUint32(b[16:], uint32(p.index))
	binary.BigEndian.PutUint32(b[20:], uint32(p.round))
	binary.BigEndian.PutUint16(b[2:], checksum(b))
	return b
}

func parseReply(b []byte, token [8]byte) (probe, bool) {
	if len(b) != echoLen || b[0] != echoReply || b[1] != 0 || [8]byte(b[8:16]) != token {
		return probe{}, false
	}
	return probe{int(binary.BigEndian.Uint32(b[16:])), int(binary.BigEndian.Uint32(b[20:]))}, true
}

func checksum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

// packetConn is an ICMP socket; datagram ones are unprivileged and use UDP addresses.
type packetConn struct {
	net.PacketConn
	datagram bool
}

func (c packetConn) send(b []byte, to netip.Addr) error {
	var dst net.Addr = &net.IPAddr{IP: to.AsSlice()}
	if c.datagram {
		dst = &net.UDPAddr{IP: to.AsSlice()}
	}
	_, err := c.WriteTo(b, dst)
	return err
}

func (c packetConn) recv(b []byte) (int, netip.Addr, error) {
	n, from, err := c.ReadFrom(b)
	if err != nil {
		return 0, netip.Addr{}, err
	}
	var ip net.IP
	switch a := from.(type) {
	case *net.UDPAddr:
		ip = a.IP
	case *net.IPAddr:
		ip = a.IP
	}
	addr, _ := netip.AddrFromSlice(ip)
	return n, addr.Unmap(), nil
}

// listen opens an unprivileged ICMP socket, or a raw one where that is not allowed.
func listen() (conn, error) {
	pc, derr := listenDatagram()
	if derr == nil {
		return packetConn{pc, true}, nil
	}
	raw, rerr := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if rerr == nil {
		if b, ok := raw.(interface{ SetReadBuffer(int) error }); ok {
			b.SetReadBuffer(readBuffer)
		}
		return packetConn{raw, false}, nil
	}
	return nil, fmt.Errorf("cannot open an ICMP socket (on Linux allow your group in net.ipv4.ping_group_range, or grant CAP_NET_RAW): %w",
		errors.Join(derr, rerr))
}

const readBuffer = 1 << 20
