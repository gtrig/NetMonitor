package config

import (
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
	if n.OfflineAfter.Duration != 15*time.Minute {
		t.Errorf("offline_after = %v", n.OfflineAfter)
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
