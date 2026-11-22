// Package config loads and validates the netmon YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/model"
	"go.yaml.in/yaml/v3"
)

const (
	DefaultListen   = "127.0.0.1:8080"
	DefaultDataDir  = "./data"
	DefaultOUIPath  = "/usr/share/nmap/nmap-mac-prefixes"
	DefaultInterval = 5 * time.Minute
)

type Config struct {
	Listen      string       `yaml:"listen"`
	DataDir     string       `yaml:"data_dir"`
	OUIPath     string       `yaml:"oui_path"`
	Auth        *BasicAuth   `yaml:"auth"`
	Routers     []Router     `yaml:"routers"`
	SNMPTargets []SNMPTarget `yaml:"snmp_targets"`
	Networks    []Network    `yaml:"networks"`
	Channels    []Channel    `yaml:"channels"`
	Rules       []Rule       `yaml:"rules"`
}

type BasicAuth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type Router struct {
	Name               string `yaml:"name"`
	Type               string `yaml:"type"`
	Address            string `yaml:"address"`
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	TLS                bool   `yaml:"tls"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

type SNMPTarget struct {
	Name      string `yaml:"name"`
	Address   string `yaml:"address"`
	Port      int    `yaml:"port"`
	Version   string `yaml:"version"`
	Community string `yaml:"community"`
	Username  string `yaml:"username"`
	AuthProto string `yaml:"auth_proto"`
	AuthPass  string `yaml:"auth_pass"`
	PrivProto string `yaml:"priv_proto"`
	PrivPass  string `yaml:"priv_pass"`
}

type Network struct {
	Name         string       `yaml:"name"`
	CIDR         netip.Prefix `yaml:"cidr"`
	Sources      []SourceRef  `yaml:"sources"`
	Exclude      []netip.Addr `yaml:"exclude"`
	OfflineAfter Duration     `yaml:"offline_after"`
}

type SourceRef struct {
	Type     string   `yaml:"type"`
	Ref      string   `yaml:"ref"`
	Interval Duration `yaml:"interval"`
	Args     []string `yaml:"args"`
}

type Channel struct {
	Name       string   `yaml:"name"`
	Type       string   `yaml:"type"`
	BotToken   string   `yaml:"bot_token"`
	ChatID     string   `yaml:"chat_id"`
	WebhookURL string   `yaml:"webhook_url"`
	SMTPHost   string   `yaml:"smtp_host"`
	SMTPPort   int      `yaml:"smtp_port"`
	Username   string   `yaml:"username"`
	Password   string   `yaml:"password"`
	From       string   `yaml:"from"`
	To         []string `yaml:"to"`
	StartTLS   bool     `yaml:"starttls"`
}

type Rule struct {
	Events      []string `yaml:"events"`
	Networks    []string `yaml:"networks"`
	Channels    []string `yaml:"channels"`
	OnlyUnknown bool     `yaml:"only_unknown"`
}

// Load reads the file at path, expands ${VAR} references, and parses it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse expands env references in data, decodes it, applies defaults and validates.
func Parse(data []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	expandNode(&root)
	expanded, err := yaml.Marshal(&root)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if v := os.Getenv("NETMON_LISTEN"); v != "" {
		c.Listen = v
	}
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if v := os.Getenv("NETMON_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if c.OUIPath == "" {
		c.OUIPath = DefaultOUIPath
	}
	for i := range c.SNMPTargets {
		if c.SNMPTargets[i].Port == 0 {
			c.SNMPTargets[i].Port = 161
		}
		if c.SNMPTargets[i].Version == "" {
			c.SNMPTargets[i].Version = "v2c"
		}
	}
	for i := range c.Channels {
		if c.Channels[i].Type == "email" && c.Channels[i].SMTPPort == 0 {
			c.Channels[i].SMTPPort = 587
		}
	}
	for i := range c.Networks {
		n := &c.Networks[i]
		for j := range n.Sources {
			if n.Sources[j].Interval.Duration == 0 {
				n.Sources[j].Interval.Duration = DefaultInterval
			}
		}
	}
}

var forbiddenNmapArgs = []string{"-p", "-sS", "-sT", "-sU", "-sV", "-O", "-A"}

// Validate checks references and values, returning all problems joined.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	routers := map[string]bool{}
	for _, r := range c.Routers {
		if r.Name == "" || routers[r.Name] {
			bad("router %q: missing or duplicate name", r.Name)
		}
		routers[r.Name] = true
		if r.Type != "mikrotik" {
			bad("router %q: unsupported type %q (only mikrotik)", r.Name, r.Type)
		}
		if r.Address == "" {
			bad("router %q: address is required", r.Name)
		}
	}
	snmp := map[string]bool{}
	for _, s := range c.SNMPTargets {
		if s.Name == "" || snmp[s.Name] {
			bad("snmp target %q: missing or duplicate name", s.Name)
		}
		snmp[s.Name] = true
		switch s.Version {
		case "v2c":
			if s.Community == "" {
				bad("snmp target %q: community is required for v2c", s.Name)
			}
		case "v3":
			if s.Username == "" {
				bad("snmp target %q: username is required for v3", s.Name)
			}
		default:
			bad("snmp target %q: unsupported version %q", s.Name, s.Version)
		}
	}
	nets := map[string]bool{}
	for _, n := range c.Networks {
		if n.Name == "" || nets[n.Name] {
			bad("network %q: missing or duplicate name", n.Name)
		}
		nets[n.Name] = true
		if !n.CIDR.IsValid() {
			bad("network %q: cidr is required", n.Name)
		}
		if len(n.Sources) == 0 {
			bad("network %q: at least one source is required", n.Name)
		}
		for _, s := range n.Sources {
			switch s.Type {
			case "router":
				if !routers[s.Ref] {
					bad("network %q: router source refers to unknown router %q", n.Name, s.Ref)
				}
			case "snmp":
				if !snmp[s.Ref] {
					bad("network %q: snmp source refers to unknown snmp target %q", n.Name, s.Ref)
				}
			case "nmap":
				for _, a := range s.Args {
					for _, f := range forbiddenNmapArgs {
						if a == f || (strings.HasPrefix(a, f) && f == "-p") {
							bad("network %q: nmap arg %q not allowed (host discovery only)", n.Name, a)
						}
					}
				}
			default:
				bad("network %q: unknown source type %q", n.Name, s.Type)
			}
		}
	}
	chans := map[string]bool{}
	for _, ch := range c.Channels {
		if ch.Name == "" || chans[ch.Name] {
			bad("channel %q: missing or duplicate name", ch.Name)
		}
		chans[ch.Name] = true
		switch ch.Type {
		case "telegram":
			if ch.BotToken == "" || ch.ChatID == "" {
				bad("channel %q: telegram needs bot_token and chat_id", ch.Name)
			}
		case "discord":
			if ch.WebhookURL == "" {
				bad("channel %q: discord needs webhook_url", ch.Name)
			}
		case "email":
			if ch.SMTPHost == "" || ch.From == "" || len(ch.To) == 0 {
				bad("channel %q: email needs smtp_host, from and to", ch.Name)
			}
		default:
			bad("channel %q: unknown type %q", ch.Name, ch.Type)
		}
	}
	for i, r := range c.Rules {
		if len(r.Channels) == 0 {
			bad("rule %d: no channels", i+1)
		}
		for _, ch := range r.Channels {
			if !chans[ch] {
				bad("rule %d: unknown channel %q", i+1, ch)
			}
		}
		for _, e := range r.Events {
			if !slices.Contains(model.EventTypes, e) {
				bad("rule %d: unknown event type %q", i+1, e)
			}
		}
		for _, n := range r.Networks {
			if !nets[n] {
				bad("rule %d: unknown network %q", i+1, n)
			}
		}
	}
	if c.Auth != nil && (c.Auth.Username == "" || c.Auth.Password == "") {
		bad("auth: username and password are required when auth is set")
	}
	return errors.Join(errs...)
}

// MissedScans is how many consecutive collections a source must miss a device before it is offline.
const MissedScans = 3

// OfflineAfterFor returns how long after a device was last seen a source's successful
// collections must keep missing it before it counts as offline: the network's explicit
// offline_after if set, else MissedScans × the source's interval.
func (n Network) OfflineAfterFor(s SourceRef) time.Duration {
	if n.OfflineAfter.Duration > 0 {
		return n.OfflineAfter.Duration
	}
	return MissedScans * s.Interval.Duration
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandNode replaces ${VAR} in scalar values with the environment value. Expansion happens
// after YAML parsing, so secrets containing YAML syntax or "$" are kept verbatim.
func expandNode(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "${") {
		v := envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			return os.Getenv(m[2 : len(m)-1])
		})
		if v != n.Value {
			n.Value, n.Tag, n.Style = v, "!!str", yaml.DoubleQuotedStyle
		}
	}
	for _, c := range n.Content {
		expandNode(c)
	}
}
