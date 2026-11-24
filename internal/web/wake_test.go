package web

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

func TestWake(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.UpsertDevices(context.Background(), []model.Device{
		{Key: "mac:aa:00:00:00:00:01", Network: "lan", MAC: "aa:00:00:00:00:01"},
		{Key: "ip:10.0.0.9", Network: "lan", IP: netip.MustParseAddr("10.0.0.9")},
		{Key: "mac:aa:00:00:00:00:02", Network: "gone", MAC: "aa:00:00:00:00:02"},
		{Key: "mac:aa:00:00:00:00:03", Network: "far", MAC: "aa:00:00:00:00:03"},
	})
	cfg := &config.Config{Routers: []config.Router{{Name: "core"}}, Networks: []config.Network{
		{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24")},
		{Name: "far", CIDR: netip.MustParsePrefix("10.9.0.0/24")},
	}}
	var sent []string
	var sendErr error
	h := New(Deps{Store: st, Runtime: func() Runtime {
		return Runtime{Scheduler: &fakeSched{}, Notifier: fakeNotifier{}, Config: cfg,
			Gateways: map[string]string{"far": "core"}}
	}, Settings: &fakeSettings{raw: &config.Config{}},
		Wake: func(_ context.Context, router *config.Router, mac string, network netip.Prefix) error {
			via := "netmon"
			if router != nil {
				via = router.Name
			}
			sent = append(sent, via+" "+mac+" "+network.String())
			return sendErr
		}})

	const ok = `{"network":"lan","key":"mac:aa:00:00:00:00:01"}`
	for _, c := range []struct {
		body string
		code int
	}{
		{"{", 400},
		{`{"network":"lan","key":"nope"}`, 404},
		{`{"network":"lan","key":"mac:aa:00:00:00:00:02"}`, 404},
		{`{"network":"lan","key":"ip:10.0.0.9"}`, 409},
		{`{"network":"gone","key":"mac:aa:00:00:00:00:02"}`, 409},
	} {
		if rr := do(h, "POST", "/api/devices/wake", c.body, false); rr.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.body, rr.Code, rr.Body, c.code)
		}
	}
	if len(sent) != 0 {
		t.Fatalf("sent %v", sent)
	}
	if rr := do(h, "POST", "/api/devices/wake", ok, false); rr.Code != 200 || len(sent) != 1 || sent[0] != "netmon aa:00:00:00:00:01 10.0.0.0/24" {
		t.Fatalf("%d %s, sent %v", rr.Code, rr.Body, sent)
	}
	rr := do(h, "POST", "/api/devices/wake", `{"network":"far","key":"mac:aa:00:00:00:00:03"}`, false)
	if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `{"via":"core"}` || len(sent) != 2 || sent[1] != "core aa:00:00:00:00:03 10.9.0.0/24" {
		t.Fatalf("router: %d %s, sent %v", rr.Code, rr.Body, sent)
	}
	sendErr = errors.New("network is unreachable")
	if rr := do(h, "POST", "/api/devices/wake", ok, false); rr.Code != 502 || !strings.Contains(rr.Body.String(), "unreachable") {
		t.Fatalf("send error: %d %s", rr.Code, rr.Body)
	}

	// The button is only offered for devices that have a MAC address.
	for path, want := range map[string]int{
		"/devices?network=lan":                          1,
		"/device?network=lan&key=mac:aa:00:00:00:00:01": 1,
		"/device?network=lan&key=ip:10.0.0.9":           0,
	} {
		rr := do(h, "GET", path, "", false)
		if n := strings.Count(rr.Body.String(), ` wake"`); rr.Code != 200 || n != want {
			t.Errorf("%s: %d, %d wake buttons, want %d", path, rr.Code, n, want)
		}
	}
}
