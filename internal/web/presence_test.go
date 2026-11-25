package web

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

func TestAsleepAndOfflineAfter(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.UpsertDevices(ctx, []model.Device{{Key: nasKey, Network: "lan", MAC: "aa:00:00:00:00:01", IP: netip.MustParseAddr("10.0.0.1"), Online: true}})
	f := &fakeSched{presence: map[model.MemberRef]model.Presence{{Network: "lan", Key: nasKey}: {Asleep: true, Limit: 990 * time.Second,
		Pattern: model.Pattern{Periodic: true, Typical: 10 * time.Minute, Spread: time.Minute, Longest: 11 * time.Minute}}}}
	cfg := &config.Config{Networks: []config.Network{{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24")}}}
	h := New(Deps{Store: st, Runtime: func() Runtime { return Runtime{Scheduler: f, Notifier: fakeNotifier{}, Config: cfg} },
		Settings: &fakeSettings{raw: &config.Config{}}})
	for path, want := range map[string]string{
		"/devices":                `<span class="tag">asleep</span>`,
		devicePath("lan", nasKey): "silent for about 10m at a time (±1m), offline after 16m30s",
		"/api/devices":            `"asleep":true`,
		"/api/graph?network=lan":  `"status":"asleep"`,
	} {
		if rr := do(h, "GET", path, "", false); rr.Code != 200 || !strings.Contains(rr.Body.String(), want) {
			t.Errorf("%s: %d, missing %q", path, rr.Code, want)
		}
	}
	get := func() time.Duration { ds, _ := st.Devices(ctx, "lan"); return ds[0].OfflineAfter }
	for _, c := range []struct {
		v    string
		code int
		want time.Duration
	}{{"15m", 204, 15 * time.Minute}, {"soon", 400, 15 * time.Minute}, {"-5m", 400, 15 * time.Minute}, {"0s", 400, 15 * time.Minute}, {"999h", 400, 15 * time.Minute}, {"", 204, 0}} {
		rr := do(h, "PATCH", "/api/devices/"+nasKey, `{"offline_after":"`+c.v+`"}`, false)
		if rr.Code != c.code || get() != c.want {
			t.Errorf("%q: %d %s, stored %v", c.v, rr.Code, rr.Body, get())
		}
	}
}
