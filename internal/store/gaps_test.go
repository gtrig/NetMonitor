package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

func TestGapsAndOfflineAfter(t *testing.T) {
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dev := func(key, mac string) model.Device {
		return model.Device{Key: key, Network: "lan", MAC: mac, Online: true, FirstSeen: now, LastSeen: now}
	}
	s.UpsertDevices(ctx, []model.Device{dev("ip:10.0.0.5", "")})
	if err := s.SetOfflineAfter(ctx, "ip:10.0.0.5", 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOfflineAfter(ctx, "ip:none", time.Minute); err != ErrNotFound {
		t.Errorf("unknown device: %v", err)
	}
	for i := range maxGaps + 5 {
		if err := s.AddGap(ctx, "lan", "ip:10.0.0.5", now, time.Duration(i)*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	s.AddGap(ctx, "lan", "other", now.Add(-time.Hour), time.Minute)
	s.Close() // both survive a restart
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check := func(network, key string, want int) {
		t.Helper()
		g, err := s.Gaps(ctx, network, now.Add(-time.Minute))
		if err != nil || len(g[key]) != want {
			t.Fatalf("%s %s: %d gaps, %v; want %d", network, key, len(g[key]), err, want)
		}
	}
	override := func(network string) {
		t.Helper()
		ds, _ := s.Devices(ctx, network)
		if len(ds) != 1 || ds[0].OfflineAfter != 15*time.Minute {
			t.Fatalf("devices = %+v", ds)
		}
	}
	check("lan", "ip:10.0.0.5", maxGaps)
	check("lan", "other", 0) // before since
	d := dev("mac:aa", "aa")
	d.PrevKey = "ip:10.0.0.5"
	s.UpsertDevices(ctx, []model.Device{d})
	check("lan", "mac:aa", maxGaps)
	override("lan")
	old := dev("mac:aa", "aa")
	old.MergedInto = "mac:bb"
	s.UpsertDevices(ctx, []model.Device{dev("mac:bb", "bb"), old})
	check("lan", "mac:bb", maxGaps)
	override("lan")
	s.RenameNetwork(ctx, "lan", "home")
	check("home", "mac:bb", maxGaps)
	override("home")
	if err := s.PruneGaps(ctx, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	check("home", "mac:bb", maxGaps)
	s.DeleteDevice(ctx, "home", "mac:bb")
	check("home", "mac:bb", 0)
}
