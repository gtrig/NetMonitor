package scheduler

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

type fakeDetails struct {
	mu      sync.Mutex
	targets [][]netip.Addr
}

func (f *fakeDetails) Name() string { return "services" }
func (f *fakeDetails) Scan(ctx context.Context, targets []netip.Addr) ([]model.HostDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, targets)
	var out []model.HostDetails
	for _, t := range targets {
		out = append(out, model.HostDetails{IP: t, OS: "Linux", Ports: []model.Port{{Proto: "tcp", Port: 22, State: "open", Service: "ssh"}}})
	}
	return out, nil
}

func seed(t *testing.T, s *Scheduler) {
	t.Helper()
	now := time.Now().UTC()
	on := model.Device{Network: "lan", Key: "mac:aa", IP: netip.MustParseAddr("10.0.0.2"), Online: true, FirstSeen: now, LastSeen: now}
	off := model.Device{Network: "lan", Key: "mac:bb", IP: netip.MustParseAddr("10.0.0.3"), Online: false, FirstSeen: now, LastSeen: now}
	if err := s.st.UpsertDevices(context.Background(), []model.Device{on, off}); err != nil {
		t.Fatal(err)
	}
}

func TestDetailsJobScansOnlineHostsWithoutEvents(t *testing.T) {
	s, st, sk, _ := setup(t, &fakeSource{})
	fd := &fakeDetails{}
	j := Job{Network: lan, Details: fd, Interval: 6 * time.Hour}
	s = New(st, sk, nil, nil, []Job{{Network: lan, Source: &fakeSource{}, Interval: time.Hour, OfflineAfter: 3 * time.Hour}, j})
	seed(t, s)
	if err := s.RunOnce(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if len(fd.targets) != 1 || len(fd.targets[0]) != 1 || fd.targets[0][0].String() != "10.0.0.2" {
		t.Fatalf("targets = %v", fd.targets)
	}
	d, _ := st.Details(context.Background(), "lan")
	if got := d[model.MemberRef{Network: "lan", Key: "mac:aa"}]; got.OS != "Linux" || len(got.Ports) != 1 {
		t.Fatalf("details = %v", d)
	}
	if len(sk.evs) != 0 {
		t.Fatalf("events = %v", sk.evs)
	}
	if _, ok := s.thresholds("lan")["services"]; ok {
		t.Fatal("services job counted for offline thresholds")
	}
	if st := s.Status()[1]; st.Source != "services" || st.LastOK.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}

func TestScanDevice(t *testing.T) {
	fd := &fakeDetails{}
	s := runSched(t, nil)
	s.DefaultDetails = fd
	seed(t, s)
	if err := s.ScanDevice("lan", "nope"); err == nil {
		t.Fatal("unknown device should fail")
	}
	if err := s.ScanDevice("lan", "mac:bb"); err != nil { // offline devices can be scanned on demand
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		d, _ := s.st.Details(context.Background(), "lan")
		_, ok := d[model.MemberRef{Network: "lan", Key: "mac:bb"}]
		return ok
	})
	waitFor(t, func() bool { return len(s.Status()) == 0 })
}
