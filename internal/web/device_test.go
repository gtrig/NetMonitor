package web

import (
	"context"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

const nasKey = "mac:aa:00:00:00:00:01"

func devicePath(network, key string) string {
	return "/device?" + url.Values{"network": {network}, "key": {key}}.Encode()
}

func TestDevicePage(t *testing.T) {
	h, st, _ := newTest(t, creds)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.SaveDetails(ctx, []model.DeviceDetails{{Network: "lan", Key: nasKey, ScannedAt: now, OS: "Linux 5.x", OSAccuracy: 97,
		Ports: []model.Port{{Proto: "tcp", Port: 22, State: "open", Service: "ssh", Product: "OpenSSH"},
			{Proto: "tcp", Port: 8443, State: "open", Service: "https"}}}}); err != nil {
		t.Fatal(err)
	}
	st.AddEvents(ctx, []model.Event{
		{Type: model.EventDeviceNew, Network: "lan", DeviceKey: nasKey, Message: "nas appeared", At: now},
		{Type: model.EventDeviceNew, Network: "lan", DeviceKey: "ip:10.0.0.9", Message: "someone else", At: now}})

	rr := do(h, "GET", devicePath("lan", nasKey), "", true)
	body := rr.Body.String()
	if rr.Code != 200 {
		t.Fatalf("%d %s", rr.Code, body)
	}
	for _, want := range []string{"nas", "10.0.0.1", "aa:00:00:00:00:01", "Linux 5.x", "OpenSSH", "https://10.0.0.1:8443/", "nas appeared", "Forget", `href="/devices" aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "someone else") {
		t.Error("shows another device's event")
	}
	if rr := do(h, "GET", devicePath("lan", "mac:ff:00:00:00:00:01"), "", true); rr.Code != 404 {
		t.Errorf("unknown device: %d", rr.Code)
	}
	if rr := do(h, "GET", devicePath("other", nasKey), "", true); rr.Code != 404 {
		t.Errorf("wrong network: %d", rr.Code)
	}
}

func TestDevicePageMachine(t *testing.T) {
	h, st, _ := newTest(t, creds)
	addDevice(t, st, "vpn", "ip:10.0.10.5", "10.0.10.5")
	ctx := context.Background()
	if _, err := st.MergeMachine(ctx, []model.MemberRef{{Network: "lan", Key: nasKey}, {Network: "vpn", Key: "ip:10.0.10.5"}}); err != nil {
		t.Fatal(err)
	}
	st.AddEvents(ctx, []model.Event{{Type: model.EventDeviceNew, Network: "vpn", DeviceKey: "ip:10.0.10.5", Message: "vpn side appeared", At: time.Now()}})
	body := do(h, "GET", devicePath("lan", nasKey), "", true).Body.String()
	for _, want := range []string{"10.0.10.5", "key=ip%3a10.0.10.5", "vpn side appeared", "Split"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestDevicePageReadOnlyAndOffline(t *testing.T) {
	h, st, _ := newTest(t, nil)
	now := time.Now().UTC()
	// No address, and a hostile hostname.
	st.UpsertDevices(context.Background(), []model.Device{{Key: "mac:aa:00:00:00:00:02", Network: "lan", MAC: "aa:00:00:00:00:02",
		Hostname: "<script>x</script>", FirstSeen: now, LastSeen: now}})
	rr := do(h, "GET", devicePath("lan", "mac:aa:00:00:00:00:02"), "", false)
	body := rr.Body.String()
	if rr.Code != 200 || strings.Contains(body, "<script>x") || strings.Contains(body, "invalid IP") || strings.Contains(body, "Forget") {
		t.Fatalf("%d %s", rr.Code, body)
	}
}

func TestDevicesListLinksToDevicePage(t *testing.T) {
	h, _, _ := newTest(t, creds)
	body := do(h, "GET", "/devices", "", true).Body.String()
	if !strings.Contains(body, `href="/device?network=lan&amp;key=mac%3aaa%3a00%3a00%3a00%3a00%3a01"`) {
		t.Fatal(body)
	}
}

func TestWebLinks(t *testing.T) {
	p := func(port int, svc string) model.Port { return model.Port{Proto: "tcp", Port: port, Service: svc} }
	ip := netip.MustParseAddr("10.0.0.1")
	for _, c := range []struct {
		ip    netip.Addr
		ports []model.Port
		want  []string
	}{
		{ip, nil, []string{"http://10.0.0.1/"}},
		{ip, []model.Port{p(22, "ssh")}, []string{"http://10.0.0.1/"}},
		{ip, []model.Port{p(80, "http"), p(443, "https"), p(5001, "ssl/http")}, []string{"http://10.0.0.1/", "https://10.0.0.1/", "https://10.0.0.1:5001/"}},
		{netip.MustParseAddr("fd00::1"), []model.Port{p(8080, "http-proxy")}, []string{"http://[fd00::1]:8080/"}},
		{netip.Addr{}, nil, nil},
	} {
		if got := webLinks(c.ip, c.ports); !slices.Equal(got, c.want) {
			t.Errorf("webLinks(%v) = %v, want %v", c.ip, got, c.want)
		}
	}
}
