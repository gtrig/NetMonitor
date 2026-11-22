package nmap

import (
	"context"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/gtrig/netmon/internal/config"
)

func TestParseXMLHosts(t *testing.T) {
	data, _ := os.ReadFile("testdata/lan.xml")
	hosts, links, err := ParseXML(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || len(links) != 0 {
		t.Fatalf("hosts=%+v links=%+v", hosts, links)
	}
	r := hosts[0]
	if r.IP.String() != "192.168.88.1" || r.MAC != "48:a9:8a:00:00:01" || r.Vendor != "Routerboard.com" || r.Hostname != "router.lan" {
		t.Fatalf("router = %+v", r)
	}
	if hosts[1].MAC != "" || hosts[1].IP.String() != "192.168.88.20" {
		t.Fatalf("second = %+v", hosts[1])
	}
}

func TestParseXMLTraceroute(t *testing.T) {
	data, _ := os.ReadFile("testdata/traceroute.xml")
	_, links, err := ParseXML(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("links = %+v", links)
	}
	if links[0].A.ID != "192.168.88.1" || links[0].B.ID != "10.0.0.1" || links[0].A.Kind != "router" {
		t.Fatalf("hop link = %+v", links[0])
	}
	if links[1].A.ID != "10.0.0.1" || links[1].B.Kind != "device" || links[1].B.ID != "ip:10.20.0.5" {
		t.Fatalf("last link = %+v", links[1])
	}
}

func TestBuildArgs(t *testing.T) {
	got := BuildArgs([]string{"-sn", "-T4"}, netip.MustParsePrefix("10.0.0.0/24"))
	want := []string{"-sn", "-oX", "-", "-T4", "10.0.0.0/24"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCollectNmapMissing(t *testing.T) {
	s := New(nil, func(ctx context.Context, args ...string) ([]byte, error) { return nil, exec.ErrNotFound })
	_, err := s.Collect(context.Background(), config.Network{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24")})
	if err == nil || !strings.Contains(err.Error(), "nmap not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectPassesArgs(t *testing.T) {
	data, _ := os.ReadFile("testdata/lan.xml")
	var gotArgs []string
	s := New([]string{"-T4"}, func(ctx context.Context, args ...string) ([]byte, error) { gotArgs = args; return data, nil })
	o, err := s.Collect(context.Background(), config.Network{Name: "lan", CIDR: netip.MustParsePrefix("192.168.88.0/24")})
	if err != nil || len(o.Hosts) != 2 || o.Source != "nmap" || gotArgs[len(gotArgs)-1] != "192.168.88.0/24" {
		t.Fatalf("o=%+v err=%v args=%v", o, err, gotArgs)
	}
}
