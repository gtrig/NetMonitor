package config

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestParseExample(t *testing.T) {
	for _, k := range []string{"SNMP_COMMUNITY", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "DISCORD_WEBHOOK_URL", "SMTP_PASSWORD"} {
		t.Setenv(k, "v")
	}
	t.Setenv("CORE_PASS", "x")
	c, err := Load("../../config/config.example.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(c.Networks[0].Sources); got != 3 {
		t.Fatalf("sources = %d, want 3", got)
	}
	if c.Routers[0].Password != "x" {
		t.Fatalf("router password = %q, want x", c.Routers[0].Password)
	}
}

const minimal = `
networks:
  - name: lan
    cidr: 192.168.88.0/24
    sources:
      - type: nmap
`

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.DataDir != "./data" {
		t.Errorf("DataDir = %q", c.DataDir)
	}
	n := c.Networks[0]
	if n.Sources[0].Interval.Duration != 5*time.Minute {
		t.Errorf("interval = %v", n.Sources[0].Interval)
	}
	if got := n.OfflineAfterFor(n.Sources[0]); got != 15*time.Minute {
		t.Errorf("offline after = %v", got)
	}
	n.OfflineAfter.Duration = time.Hour
	if got := n.OfflineAfterFor(n.Sources[0]); got != time.Hour {
		t.Errorf("explicit offline_after = %v", got)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("NETMON_LISTEN", "0.0.0.0:9000")
	t.Setenv("NETMON_DATA_DIR", "/data")
	c, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:9000" || c.DataDir != "/data" {
		t.Fatalf("Listen=%q DataDir=%q", c.Listen, c.DataDir)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"unknown source type", `
networks:
  - name: lan
    cidr: 10.0.0.0/24
    sources: [{type: telnet}]`, "telnet"},
		{"missing router ref", `
networks:
  - name: lan
    cidr: 10.0.0.0/24
    sources: [{type: router, ref: core}]`, "core"},
		{"duplicate network", `
networks:
  - {name: lan, cidr: 10.0.0.0/24, sources: [{type: nmap}]}
  - {name: lan, cidr: 10.0.1.0/24, sources: [{type: nmap}]}`, "lan"},
		{"rule unknown channel", `
networks:
  - {name: lan, cidr: 10.0.0.0/24, sources: [{type: nmap}]}
rules:
  - {events: [device_new], channels: [pager]}`, "pager"},
		{"rule unknown event", `
networks:
  - {name: lan, cidr: 10.0.0.0/24, sources: [{type: nmap}]}
channels:
  - {name: tg, type: telegram, bot_token: a, chat_id: b}
rules:
  - {events: [device_exploded], channels: [tg]}`, "device_exploded"},
		{"nmap port scan arg", `
networks:
  - {name: lan, cidr: 10.0.0.0/24, sources: [{type: nmap, args: ["-p", "22"]}]}`, "-p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Finding 9: secrets are expanded inside values, never re-parsed as YAML.
func TestEnvExpansionKeepsSpecialCharacters(t *testing.T) {
	t.Setenv("RPASS", `a #b: c "d"`)
	c, err := Parse([]byte(`
routers:
  - {name: core, type: mikrotik, address: 1.2.3.4:8728, username: u, password: "${RPASS}"}
  - {name: ap, type: mikrotik, address: 1.2.3.5:8728, username: u, password: pa$$w0rd}
networks:
  - {name: lan, cidr: 10.0.0.0/24, sources: [{type: router, ref: core}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Routers[0].Password != `a #b: c "d"` || c.Routers[1].Password != "pa$$w0rd" {
		t.Fatalf("passwords = %q, %q", c.Routers[0].Password, c.Routers[1].Password)
	}
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTracerouteOnlyForNmap(t *testing.T) {
	_, err := Parse([]byte(`
routers:
  - {name: core, type: mikrotik, address: 1.2.3.4:8728, username: u, password: p}
networks:
  - {name: lan, cidr: 10.0.0.0/24, sources: [{type: router, ref: core, traceroute: true}]}
`))
	if err == nil || !strings.Contains(err.Error(), "traceroute") {
		t.Fatalf("err = %v", err)
	}
	c, err := Parse([]byte(`
networks:
  - {name: lan, cidr: 10.0.0.0/24, sources: [{type: nmap, traceroute: true}]}
`))
	if err != nil || !c.Networks[0].Sources[0].Traceroute {
		t.Fatalf("nmap traceroute: %v", err)
	}
}
