package mikrotik

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// wakeRouter fakes a router with addresses; it records what is run on it.
type wakeRouter struct {
	addrs            []map[string]string
	addrErr, wakeErr error
	ran              [][]string
}

func (f *wakeRouter) Run(ctx context.Context, path string, args ...string) ([]map[string]string, error) {
	f.ran = append(f.ran, append([]string{path}, args...))
	if path == "/ip/address/print" {
		return f.addrs, f.addrErr
	}
	return nil, f.wakeErr
}

func addr(a, iface string, extra ...string) map[string]string {
	m := map[string]string{"address": a, "interface": iface}
	for i := 0; i < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

var wakeNet = netip.MustParsePrefix("192.168.88.0/24")

func TestWakeInterface(t *testing.T) {
	for name, c := range map[string]struct {
		addrs []map[string]string
		want  string
	}{
		"exact":            {[]map[string]string{addr("10.0.10.3/24", "wg"), addr("192.168.88.1/24", "bridge")}, "bridge"},
		"actual interface": {[]map[string]string{addr("192.168.88.1/24", "ether2", "actual-interface", "bridge")}, "bridge"},
		"exact over wider": {[]map[string]string{addr("192.168.1.1/16", "other"), addr("192.168.88.1/24", "bridge")}, "bridge"},
		"wider":            {[]map[string]string{addr("192.168.1.1/16", "lan")}, "lan"},
		"skips unusable": {[]map[string]string{addr("192.168.88.9/24", "old", "disabled", "true"),
			addr("192.168.88.8/24", "gone", "invalid", "true"), addr("192.168.88.1/32", "ppp"), addr("bad", "x"), addr("192.168.88.1/24", "bridge")}, "bridge"},
	} {
		f := &wakeRouter{addrs: c.addrs}
		if err := wake(context.Background(), f, "core", "aa:bb:cc:00:11:22", wakeNet); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want := []string{"/tool/wol", "=interface=" + c.want, "=mac=aa:bb:cc:00:11:22"}
		if got := f.ran[len(f.ran)-1]; !slices.Equal(got, want) {
			t.Errorf("%s: ran %v", name, got)
		}
	}
}

func TestWakeErrors(t *testing.T) {
	ok := []map[string]string{addr("192.168.88.1/24", "bridge")}
	for name, c := range map[string]struct {
		f    *wakeRouter
		want string
	}{
		"no address":  {&wakeRouter{addrs: []map[string]string{addr("10.0.0.1/24", "wg")}}, "no address in 192.168.88.0/24"},
		"unreachable": {&wakeRouter{addrErr: errors.New("connection refused")}, "connection refused"},
		"denied":      {&wakeRouter{addrs: ok, wakeErr: errors.New("from RouterOS device: not enough permissions (9)")}, `"test" policy`},
		"refused":     {&wakeRouter{addrs: ok, wakeErr: errors.New("bad mac")}, "bad mac"},
	} {
		err := wake(context.Background(), c.f, "core", "aa:bb:cc:00:11:22", wakeNet)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "core") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
