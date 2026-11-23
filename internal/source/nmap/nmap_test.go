package nmap

import (
	"context"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/source"
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
	if !r.NameFromDNS || r.IP.String() != "192.168.88.1" || r.MAC != "48:a9:8a:00:00:01" || r.Vendor != "Routerboard.com" || r.Hostname != "router.lan" {
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
	got := BuildArgs([]string{"-sn", "-T4"}, false, netip.MustParsePrefix("10.0.0.0/24"))
	want := []string{"-sn", "-v", "-oX", "-", "--stats-every", "2s", "-T4", "10.0.0.0/24"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCollectNmapMissing(t *testing.T) {
	s := New(nil, false, func(ctx context.Context, args ...string) ([]byte, error) { return nil, exec.ErrNotFound })
	_, err := s.Collect(context.Background(), config.Network{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24")})
	if err == nil || !strings.Contains(err.Error(), "nmap not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectPassesArgs(t *testing.T) {
	data, _ := os.ReadFile("testdata/lan.xml")
	var gotArgs []string
	s := New([]string{"-T4"}, false, func(ctx context.Context, args ...string) ([]byte, error) { gotArgs = args; return data, nil })
	o, err := s.Collect(context.Background(), config.Network{Name: "lan", CIDR: netip.MustParsePrefix("192.168.88.0/24")})
	if err != nil || len(o.Hosts) != 2 || o.Source != "nmap" || gotArgs[len(gotArgs)-1] != "192.168.88.0/24" {
		t.Fatalf("o=%+v err=%v args=%v", o, err, gotArgs)
	}
}

func TestBuildArgsTraceroute(t *testing.T) {
	got := BuildArgs([]string{"--traceroute"}, true, netip.MustParsePrefix("10.0.0.0/24"))
	if n := slices.Index(got, "--traceroute"); n < 0 || slices.Index(got[n+1:], "--traceroute") >= 0 {
		t.Fatalf("want exactly one --traceroute: %v", got)
	}
}

func TestParseProgress(t *testing.T) {
	data, _ := os.ReadFile("testdata/progress.txt")
	var got []Progress
	for _, l := range strings.Split(string(data), "\n") {
		if p, ok := ParseProgress(l); ok {
			got = append(got, p)
		}
	}
	if len(got) != 4 || got[0].Task != "Ping Scan" || got[0].Percent != -1 || got[1].Percent != 23.45 ||
		got[2].Task != "Parallel DNS resolution of 256 hosts" || got[2].Percent != -1 || got[3].Task != "Traceroute" || got[3].Percent != 50 {
		t.Fatalf("got %+v", got)
	}
}

func TestParseXMLWithTaskprogress(t *testing.T) {
	data, _ := os.ReadFile("testdata/traceroute.xml")
	s := strings.Replace(string(data), "<host", `<taskprogress task="Ping Scan" time="1" percent="10.00" remaining="5" etime="6"/>
<host`, 1)
	hosts, links, err := ParseXML([]byte(s))
	if err != nil || len(hosts) == 0 || len(links) != 2 {
		t.Fatalf("hosts=%d links=%d err=%v", len(hosts), len(links), err)
	}
}

func TestCollectReportsProgress(t *testing.T) {
	data, _ := os.ReadFile("testdata/lan.xml")
	var got []Progress
	ctx := source.WithProgress(context.Background(), func(p source.Progress) { got = append(got, Progress(p)) })
	s := New(nil, false, func(ctx context.Context, args ...string) ([]byte, error) {
		source.ReportProgress(ctx, source.Progress{Task: "Ping Scan", Percent: 42})
		return data, nil
	})
	if _, err := s.Collect(ctx, config.Network{Name: "lan", CIDR: netip.MustParsePrefix("192.168.88.0/24")}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Percent != 42 {
		t.Fatalf("progress = %+v", got)
	}
}

func TestParseSRTT(t *testing.T) {
	hosts, _, err := ParseXML([]byte(`<nmaprun><host><status state="up"/><address addr="10.0.0.1" addrtype="ipv4"/><times srtt="224" rttvar="5000" to="100000"/></host>
		<host><status state="up"/><address addr="10.0.0.2" addrtype="ipv4"/></host></nmaprun>`))
	if err != nil || len(hosts) != 2 || hosts[0].RTT != 224*time.Microsecond || hosts[1].RTT != 0 {
		t.Fatalf("hosts = %+v, %v", hosts, err)
	}
}
