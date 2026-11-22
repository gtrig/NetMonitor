package store

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "netmon.db")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func sampleDevice() model.Device {
	now := time.Now().Truncate(time.Second).UTC()
	return model.Device{
		Key: "mac:aa:bb:cc:00:11:22", Network: "lan", MAC: "aa:bb:cc:00:11:22",
		IP: netip.MustParseAddr("10.0.0.5"), Hostname: "nas", Vendor: "Synology", Port: "ether2",
		Online: true, FirstSeen: now.Add(-time.Hour), LastSeen: now, Sources: []string{"nmap", "router:core"},
	}
}

func TestDeviceRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	d := sampleDevice()
	if err := s.UpsertDevices(ctx, []model.Device{d}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Devices(ctx, "lan")
	if err != nil || len(got) != 1 {
		t.Fatalf("Devices = %v, %v", got, err)
	}
	g := got[0]
	if g.Key != d.Key || g.IP != d.IP || g.Hostname != d.Hostname || g.Port != d.Port || !g.Online ||
		!g.LastSeen.Equal(d.LastSeen) || !g.FirstSeen.Equal(d.FirstSeen) || len(g.Sources) != 2 {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", g, d)
	}
	// Upsert again with a change; meta must survive.
	label, known := "My NAS", true
	if err := s.SetDeviceMeta(ctx, d.Key, &label, &known); err != nil {
		t.Fatal(err)
	}
	d.Hostname = "nas2"
	s.UpsertDevices(ctx, []model.Device{d})
	got, _ = s.Devices(ctx, "")
	if got[0].Hostname != "nas2" || got[0].Label != "My NAS" || !got[0].Known {
		t.Fatalf("after upsert: %+v", got[0])
	}
}

func TestSetDeviceMetaNotFound(t *testing.T) {
	s, _ := openTemp(t)
	k := true
	if err := s.SetDeviceMeta(context.Background(), "mac:nope", nil, &k); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestEventsNewestFirstAndLimit(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	var evs []model.Event
	for i := 0; i < 5; i++ {
		evs = append(evs, model.Event{Type: model.EventDeviceNew, Network: "lan", At: time.Now()})
	}
	if err := s.AddEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	got, err := s.Events(ctx, EventFilter{Limit: 2})
	if err != nil || len(got) != 2 || got[0].ID != 5 || got[1].ID != 4 {
		t.Fatalf("got %+v, %v", got, err)
	}
	got, _ = s.Events(ctx, EventFilter{Before: 4, Limit: 10})
	if len(got) != 3 || got[0].ID != 3 {
		t.Fatalf("before: %+v", got)
	}
}

func TestReplaceLinksScopedBySource(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a := model.Link{A: model.NodeRef{Kind: "router", ID: "core"}, B: model.NodeRef{Kind: "router", ID: "ap"}, Source: "A"}
	b := model.Link{A: model.NodeRef{Kind: "router", ID: "core"}, B: model.NodeRef{Kind: "device", ID: "ip:1.2.3.4"}, Source: "B"}
	s.ReplaceLinks(ctx, "lan", "A", []model.Link{a})
	s.ReplaceLinks(ctx, "lan", "B", []model.Link{b})
	s.ReplaceLinks(ctx, "lan", "B", nil)
	got, err := s.Links(ctx, "lan")
	if err != nil || len(got) != 1 || got[0].B.ID != "ap" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLastSuccess(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if m, err := s.LastSuccesses(ctx, "lan"); err != nil || len(m) != 0 {
		t.Fatalf("initial = %v, %v", m, err)
	}
	now := time.Now().Truncate(time.Second)
	s.SetLastSuccess(ctx, "lan", "nmap", now)
	s.SetLastSuccess(ctx, "lan", "router:core", now.Add(time.Minute))
	s.SetLastSuccess(ctx, "iot", "nmap", now)
	m, _ := s.LastSuccesses(ctx, "lan")
	if len(m) != 2 || !m["nmap"].Equal(now) || !m["router:core"].Equal(now.Add(time.Minute)) {
		t.Fatalf("got %v", m)
	}
}

func TestReopenKeepsData(t *testing.T) {
	s, p := openTemp(t)
	s.UpsertDevices(context.Background(), []model.Device{sampleDevice()})
	s.Close()
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, _ := s2.Devices(context.Background(), "")
	if len(got) != 1 {
		t.Fatalf("got %d devices", len(got))
	}
}

func TestDevicesSortedByIP(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	var devs []model.Device
	for _, a := range []string{"10.0.0.110", "10.0.0.2", "10.0.0.20"} {
		d := sampleDevice()
		d.IP = netip.MustParseAddr(a)
		d.Key = "ip:" + a
		devs = append(devs, d)
	}
	s.UpsertDevices(ctx, devs)
	got, _ := s.Devices(ctx, "lan")
	if got[0].IP.String() != "10.0.0.2" || got[1].IP.String() != "10.0.0.20" || got[2].IP.String() != "10.0.0.110" {
		t.Fatalf("order = %v %v %v", got[0].IP, got[1].IP, got[2].IP)
	}
}

func TestUpsertWithPrevKeyRenamesAndKeepsMeta(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	d := sampleDevice()
	d.Key, d.MAC = "ip:10.0.0.5", ""
	s.UpsertDevices(ctx, []model.Device{d})
	label, known := "My NAS", true
	s.SetDeviceMeta(ctx, "ip:10.0.0.5", &label, &known)
	d.PrevKey, d.Key, d.MAC = "ip:10.0.0.5", "mac:aa:bb:cc:00:11:22", "aa:bb:cc:00:11:22"
	if err := s.UpsertDevices(ctx, []model.Device{d}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Devices(ctx, "lan")
	if len(got) != 1 || got[0].Key != "mac:aa:bb:cc:00:11:22" || got[0].Label != "My NAS" || !got[0].Known {
		t.Fatalf("got %+v", got)
	}
}
