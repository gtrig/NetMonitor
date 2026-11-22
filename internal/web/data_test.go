package web

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

func addDevice(t *testing.T, st *store.Store, network, key, ip string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.UpsertDevices(context.Background(), []model.Device{{Key: key, Network: network,
		IP: netip.MustParseAddr(ip), Online: true, FirstSeen: now, LastSeen: now}}); err != nil {
		t.Fatal(err)
	}
}

func TestDataNetworksAPI(t *testing.T) {
	h, st, _ := newTest(t, creds)
	addDevice(t, st, "old", "ip:10.9.0.1", "10.9.0.1")
	rr := do(h, "GET", "/api/data/networks", "", true)
	var got []struct {
		Name       string `json:"name"`
		Devices    int    `json:"devices"`
		Configured bool   `json:"configured"`
	}
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &got) != nil {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	m := map[string]bool{}
	for _, n := range got {
		m[n.Name] = n.Configured
	}
	if len(got) != 2 || !m["lan"] || m["old"] {
		t.Fatalf("got %+v", got)
	}
}

func TestMergeStaleNetworkRequiresEdit(t *testing.T) {
	h, st, _ := newTest(t, nil)
	addDevice(t, st, "old", "ip:10.9.0.1", "10.9.0.1")
	if rr := do(h, "POST", "/api/data/networks/old/merge", `{"into":"lan"}`, false); rr.Code != 403 {
		t.Fatalf("without auth: %d", rr.Code)
	}

	h, st, _ = newTest(t, creds)
	addDevice(t, st, "old", "ip:10.9.0.1", "10.9.0.1")
	if rr := do(h, "POST", "/api/data/networks/lan/merge", `{"into":"lan"}`, true); rr.Code != 409 {
		t.Fatalf("configured source: %d", rr.Code)
	}
	if rr := do(h, "POST", "/api/data/networks/old/merge", `{"into":"nope"}`, true); rr.Code != 400 {
		t.Fatalf("unknown target: %d", rr.Code)
	}
	if rr := do(h, "POST", "/api/data/networks/old/merge", `{"into":"lan"}`, true); rr.Code != 204 {
		t.Fatalf("merge: %d %s", rr.Code, rr.Body)
	}
	if ds, _ := st.Devices(context.Background(), "lan"); len(ds) != 2 {
		t.Fatalf("lan devices = %d", len(ds))
	}
	addDevice(t, st, "gone", "ip:10.8.0.1", "10.8.0.1")
	if rr := do(h, "DELETE", "/api/data/networks/lan", "", true); rr.Code != 409 {
		t.Fatalf("delete configured: %d", rr.Code)
	}
	if rr := do(h, "DELETE", "/api/data/networks/gone", "", true); rr.Code != 204 {
		t.Fatalf("delete: %d", rr.Code)
	}
	if ds, _ := st.Devices(context.Background(), "gone"); len(ds) != 0 {
		t.Fatal("not deleted")
	}
}

func TestDeleteDeviceAPI(t *testing.T) {
	h, st, _ := newTest(t, creds)
	if rr := do(h, "DELETE", "/api/devices/mac:aa:00:00:00:00:01?network=lan", "", true); rr.Code != 204 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	if ds, _ := st.Devices(context.Background(), "lan"); len(ds) != 0 {
		t.Fatal("not deleted")
	}
	if rr := do(h, "DELETE", "/api/devices/mac:aa:00:00:00:00:01?network=lan", "", true); rr.Code != 404 {
		t.Fatalf("second delete: %d", rr.Code)
	}
}

func TestMachinesAPI(t *testing.T) {
	h, st, _ := newTest(t, creds)
	addDevice(t, st, "vpn", "ip:10.0.10.5", "10.0.10.5")
	body := `{"members":[{"network":"lan","key":"mac:aa:00:00:00:00:01"},{"network":"vpn","key":"ip:10.0.10.5"}]}`
	if rr := do(h, "POST", "/api/machines", `{"members":[{"network":"lan","key":"mac:aa:00:00:00:00:01"}]}`, true); rr.Code != 400 {
		t.Fatalf("one member: %d", rr.Code)
	}
	rr := do(h, "POST", "/api/machines", body, true)
	if rr.Code != 200 {
		t.Fatalf("merge: %d %s", rr.Code, rr.Body)
	}
	machines := func() map[string]int64 {
		var ds []struct {
			Key     string `json:"key"`
			Machine int64  `json:"machine"`
		}
		json.Unmarshal(do(h, "GET", "/api/devices", "", true).Body.Bytes(), &ds)
		m := map[string]int64{}
		for _, d := range ds {
			m[d.Key] = d.Machine
		}
		return m
	}
	m := machines()
	if m["ip:10.0.10.5"] == 0 || m["ip:10.0.10.5"] != m["mac:aa:00:00:00:00:01"] {
		t.Fatalf("machines = %v", m)
	}
	if rr := do(h, "POST", "/api/machines/split", `{"network":"vpn","key":"ip:10.0.10.5"}`, true); rr.Code != 204 {
		t.Fatalf("split: %d", rr.Code)
	}
	if m := machines(); m["ip:10.0.10.5"] != 0 || m["mac:aa:00:00:00:00:01"] != 0 {
		t.Fatalf("after split = %v", m)
	}
}
