package store

import (
	"context"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

func details(network, key string, at time.Time, ports ...int) model.DeviceDetails {
	d := model.DeviceDetails{Network: network, Key: key, ScannedAt: at.Truncate(time.Second).UTC(), OS: "Linux 5.X", OSAccuracy: 95}
	for _, p := range ports {
		d.Ports = append(d.Ports, model.Port{Proto: "tcp", Port: p, State: "open", Service: "ssh", Product: "OpenSSH", Version: "9.6"})
	}
	return d
}

func TestDetailsRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SaveDetails(ctx, []model.DeviceDetails{details("lan", "mac:aa", now, 22, 80)}); err != nil {
		t.Fatal(err)
	}
	// replaced, not appended
	s.SaveDetails(ctx, []model.DeviceDetails{details("lan", "mac:aa", now, 443)})
	got, err := s.Details(ctx, "lan")
	if err != nil {
		t.Fatal(err)
	}
	d := got[model.MemberRef{Network: "lan", Key: "mac:aa"}]
	if len(d.Ports) != 1 || d.Ports[0].Port != 443 || d.Ports[0].Product != "OpenSSH" || d.OS != "Linux 5.X" || d.OSAccuracy != 95 ||
		!d.ScannedAt.Equal(now.Truncate(time.Second).UTC()) {
		t.Fatalf("details = %+v", d)
	}
	if all, _ := s.Details(ctx, ""); len(all) != 1 {
		t.Fatalf("all = %v", all)
	}
}

func TestDetailsFollowDevice(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Now()
	mustUpsert(t, s, dev("lan", "ip:10.0.0.2", "10.0.0.2"), dev("lan", "mac:bb", "10.0.0.3"))
	s.SaveDetails(ctx, []model.DeviceDetails{details("lan", "ip:10.0.0.2", now, 22), details("lan", "mac:bb", now, 80)})

	d := dev("lan", "mac:aa", "10.0.0.2")
	d.PrevKey = "ip:10.0.0.2"
	mustUpsert(t, s, d)
	got, _ := s.Details(ctx, "lan")
	if _, ok := got[model.MemberRef{Network: "lan", Key: "mac:aa"}]; !ok || len(got) != 2 {
		t.Fatalf("after PrevKey: %v", got)
	}

	if err := s.DeleteDevice(ctx, "lan", "mac:bb"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Details(ctx, "lan"); len(got) != 1 {
		t.Fatalf("after delete: %v", got)
	}

	s.RenameNetwork(ctx, "lan", "home")
	if got, _ := s.Details(ctx, "home"); len(got) != 1 {
		t.Fatalf("after rename: %v", got)
	}
	s.DeleteNetworkData(ctx, "home")
	if got, _ := s.Details(ctx, ""); len(got) != 0 {
		t.Fatalf("after network delete: %v", got)
	}
}
