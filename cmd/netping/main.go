// Command netping lists the active hosts of IPv4 networks.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gtrig/netmon/pinger"
)

type host struct {
	IP       netip.Addr `json:"ip"`
	MinMS    float64    `json:"min_ms"`
	MaxMS    float64    `json:"max_ms"`
	AvgMS    float64    `json:"avg_ms"`
	Received int        `json:"received"`
	Sent     int        `json:"sent"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// parseTargets expands addresses and networks, without duplicates.
func parseTargets(args []string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, arg := range args {
		p, err := parseTarget(arg)
		if err != nil {
			return nil, err
		}
		hosts, err := pinger.Hosts(p)
		if err != nil {
			return nil, err
		}
		out = append(out, hosts...)
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out), nil
}

// parseTarget reads an address, or an address with its netmask: /24 or /255.255.255.0.
func parseTarget(s string) (netip.Prefix, error) {
	addr, mask, hasMask := strings.Cut(s, "/")
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is4() {
		return netip.Prefix{}, fmt.Errorf("%q is not an IPv4 address or network", s)
	}
	if !hasMask {
		return netip.PrefixFrom(a, 32), nil
	}
	bits, err := strconv.Atoi(mask)
	if err != nil {
		m, merr := netip.ParseAddr(mask)
		if merr != nil || !m.Is4() {
			return netip.Prefix{}, fmt.Errorf("%q: %q is not a netmask (use 24 or 255.255.255.0)", s, mask)
		}
		ones, size := net.IPMask(m.AsSlice()).Size()
		if size == 0 {
			return netip.Prefix{}, fmt.Errorf("%q: %q is not a valid netmask", s, mask)
		}
		bits = ones
	}
	if bits < 0 || bits > 32 {
		return netip.Prefix{}, fmt.Errorf("%q: netmask /%d is out of range", s, bits)
	}
	return netip.PrefixFrom(a, bits).Masked(), nil
}

func main() {
	os.Exit(run())
}

func run() int {
	count := flag.Int("count", pinger.DefaultCount, "pings per host")
	timeout := flag.Duration("timeout", pinger.DefaultTimeout, "wait for replies after the last ping")
	gap := flag.Duration("gap", pinger.DefaultGap, "minimum time between pings of one host")
	rate := flag.Int("rate", pinger.DefaultRate, "packets per second")
	asJSON := flag.Bool("json", false, "print JSON")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: netping [flags] IP|NETWORK/NETMASK ...\n  e.g. netping 192.168.8.12\n       netping -count 5 192.168.8.1/24 192.168.88.1/24")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 || *timeout <= 0 || *gap <= 0 || *rate < 1 {
		flag.Usage()
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "netping:", err)
		return 2
	}
	if *count < 1 || *count > pinger.MaxCount {
		return fail(fmt.Errorf("-count must be from 1 to %d", pinger.MaxCount))
	}
	addrs, err := parseTargets(flag.Args())
	if err != nil {
		return fail(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	found, err := pinger.Sweep(ctx, addrs, pinger.Options{Count: *count, Timeout: *timeout, Gap: *gap, Rate: *rate})
	took := time.Since(start)

	out := make([]host, 0, len(found))
	for a, st := range found {
		out = append(out, host{a, ms(st.Min), ms(st.Max), ms(st.Avg), st.Received, st.Sent})
	}
	slices.SortFunc(out, func(a, b host) int { return a.IP.Compare(b.IP) })
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(out)
	} else {
		fmt.Printf("%-15s  %9s  %9s  %9s  %s\n", "IP", "MIN ms", "MAX ms", "AVG ms", "REPLIES")
		for _, h := range out {
			fmt.Printf("%-15s  %9.2f  %9.2f  %9.2f  %d/%d\n", h.IP, h.MinMS, h.MaxMS, h.AvgMS, h.Received, h.Sent)
		}
	}
	fmt.Fprintf(os.Stderr, "%d of %d addresses active in %.2fs\n", len(out), len(addrs), took.Seconds())
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "netping:", err)
		} else {
			fmt.Fprintln(os.Stderr, "netping: interrupted, results are incomplete")
		}
		return 1
	}
	return 0
}
