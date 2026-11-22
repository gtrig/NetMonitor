package app

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/scheduler"
	"github.com/gtrig/netmon/internal/store"
)

type nopSource struct{}

func (nopSource) Name() string { return "nop" }
func (nopSource) Collect(ctx context.Context, n config.Network) (model.Observations, error) {
	return model.Observations{Network: n.Name, Source: "nop", At: time.Now()}, nil
}

func fakeBuild(cfg *config.Config) ([]scheduler.Job, map[string]string) {
	var jobs []scheduler.Job
	for _, n := range cfg.Networks {
		jobs = append(jobs, scheduler.Job{Network: n, Source: nopSource{}, Interval: time.Hour, OfflineAfter: 3 * time.Hour})
	}
	return jobs, map[string]string{}
}

const initial = `networks:
  - name: lan
    cidr: 10.0.0.0/24
    sources: [{type: nmap}]
`

func setup(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte(initial), 0o640)
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := New(context.Background(), p, Options{Store: st, Build: fakeBuild})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, p
}

func TestSaveWritesFileAndSwapsRuntime(t *testing.T) {
	a, p := setup(t)
	old := a.Current()
	raw := a.Raw()
	raw.Networks = append(raw.Networks, config.Network{Name: "iot", CIDR: netip.MustParsePrefix("10.0.1.0/24"),
		Sources: []config.SourceRef{{Type: "nmap"}}})
	if err := a.Save(raw, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(a.Current().Config.Networks); got != 2 {
		t.Fatalf("networks = %d", got)
	}
	select {
	case <-old.done:
	case <-time.After(2 * time.Second):
		t.Fatal("old runtime not stopped")
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "10.0.1.0/24") {
		t.Fatalf("file = %s", data)
	}
}

func TestSaveInvalidKeepsFileAndRuntime(t *testing.T) {
	a, p := setup(t)
	old := a.Current()
	raw := a.Raw()
	raw.Networks[0].Sources[0] = config.SourceRef{Type: "router", Ref: "ghost"}
	err := a.Save(raw, nil)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v", err)
	}
	if a.Current() != old {
		t.Fatal("runtime swapped on invalid save")
	}
	if data, _ := os.ReadFile(p); string(data) != initial {
		t.Fatalf("file changed: %s", data)
	}
}

func TestReloadPicksUpHandEdits(t *testing.T) {
	a, p := setup(t)
	os.WriteFile(p, []byte(initial+`  - name: dmz
    cidr: 10.0.2.0/24
    sources: [{type: nmap}]
`), 0o640)
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(a.Current().Config.Networks) != 2 || len(a.Raw().Networks) != 2 {
		t.Fatal("reload did not apply")
	}
}

func TestReloadInvalidFileKeepsRuntime(t *testing.T) {
	a, p := setup(t)
	old := a.Current()
	os.WriteFile(p, []byte("networks: [{name: lan}]\n"), 0o640)
	if err := a.Reload(); err == nil {
		t.Fatal("expected error")
	}
	if a.Current() != old || len(a.Raw().Networks[0].Sources) != 1 {
		t.Fatal("state changed on invalid reload")
	}
}

func TestRawIsACopy(t *testing.T) {
	a, _ := setup(t)
	r := a.Raw()
	r.Networks[0].Name = "changed"
	if a.Raw().Networks[0].Name != "lan" {
		t.Fatal("Raw returned shared state")
	}
}

func TestSaveRenamesNetworkData(t *testing.T) {
	a, _ := setup(t)
	st := a.opts.Store
	ctx := context.Background()
	now := time.Now().UTC()
	st.UpsertDevices(ctx, []model.Device{{Network: "lan", Key: "mac:aa", FirstSeen: now, LastSeen: now}})
	raw := a.Raw()
	raw.Networks[0].Name = "home"
	if err := a.Save(raw, map[string]string{"lan": "home"}); err != nil {
		t.Fatal(err)
	}
	if ds, _ := st.Devices(ctx, "home"); len(ds) != 1 {
		t.Fatalf("devices under new name = %d", len(ds))
	}
	if ds, _ := st.Devices(ctx, "lan"); len(ds) != 0 {
		t.Fatalf("devices left under old name = %d", len(ds))
	}
}
