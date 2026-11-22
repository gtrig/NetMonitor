// Package config loads and validates the netmon YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
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
	Listen      string       `yaml:"listen,omitempty" json:"listen,omitempty"`
	DataDir     string       `yaml:"data_dir,omitempty" json:"data_dir,omitempty"`
	OUIPath     string       `yaml:"oui_path,omitempty" json:"oui_path,omitempty"`
	Auth        *BasicAuth   `yaml:"auth,omitempty" json:"auth,omitempty"`
	Routers     []Router     `yaml:"routers,omitempty" json:"routers,omitempty"`
	SNMPTargets []SNMPTarget `yaml:"snmp_targets,omitempty" json:"snmp_targets,omitempty"`
	Networks    []Network    `yaml:"networks,omitempty" json:"networks,omitempty"`
	Channels    []Channel    `yaml:"channels,omitempty" json:"channels,omitempty"`
	Rules       []Rule       `yaml:"rules,omitempty" json:"rules,omitempty"`
}

type BasicAuth struct {
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
}

type Router struct {
	// OriginalName is set by the settings UI so renamed items keep their secrets; never saved.
	OriginalName       string `yaml:"-" json:"original_name,omitempty"`
	Name               string `yaml:"name,omitempty" json:"name,omitempty"`
	Type               string `yaml:"type,omitempty" json:"type,omitempty"`
	Address            string `yaml:"address,omitempty" json:"address,omitempty"`
	Username           string `yaml:"username,omitempty" json:"username,omitempty"`
	Password           string `yaml:"password,omitempty" json:"password,omitempty"`
	TLS                bool   `yaml:"tls,omitempty" json:"tls,omitempty"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify,omitempty" json:"insecure_skip_verify,omitempty"`
}

type SNMPTarget struct {
	// OriginalName is set by the settings UI so renamed items keep their secrets; never saved.
	OriginalName string `yaml:"-" json:"original_name,omitempty"`
	Name         string `yaml:"name,omitempty" json:"name,omitempty"`
	Address      string `yaml:"address,omitempty" json:"address,omitempty"`
	Port         int    `yaml:"port,omitempty" json:"port,omitempty"`
	Version      string `yaml:"version,omitempty" json:"version,omitempty"`
	Community    string `yaml:"community,omitempty" json:"community,omitempty"`
	Username     string `yaml:"username,omitempty" json:"username,omitempty"`
	AuthProto    string `yaml:"auth_proto,omitempty" json:"auth_proto,omitempty"`
	AuthPass     string `yaml:"auth_pass,omitempty" json:"auth_pass,omitempty"`
	PrivProto    string `yaml:"priv_proto,omitempty" json:"priv_proto,omitempty"`
	PrivPass     string `yaml:"priv_pass,omitempty" json:"priv_pass,omitempty"`
}

type Network struct {
	// OriginalName is set by the settings UI so a rename can carry stored data; never saved.
	OriginalName string       `yaml:"-" json:"original_name,omitempty"`
	Name         string       `yaml:"name,omitempty" json:"name,omitempty"`
	CIDR         netip.Prefix `yaml:"cidr" json:"cidr"`
	Sources      []SourceRef  `yaml:"sources,omitempty" json:"sources,omitempty"`
	Exclude      []netip.Addr `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	OfflineAfter Duration     `yaml:"offline_after,omitempty" json:"offline_after,omitempty"`
}

type SourceRef struct {
	Type     string   `yaml:"type,omitempty" json:"type,omitempty"`
	Ref      string   `yaml:"ref,omitempty" json:"ref,omitempty"`
	Interval Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Args     []string `yaml:"args,omitempty" json:"args,omitempty"`
	// Traceroute (nmap only) records the routers on the path to each host.
	Traceroute bool `yaml:"traceroute,omitempty" json:"traceroute,omitempty"`
}

type Channel struct {
	// OriginalName is set by the settings UI so renamed items keep their secrets; never saved.
	OriginalName string   `yaml:"-" json:"original_name,omitempty"`
	Name         string   `yaml:"name,omitempty" json:"name,omitempty"`
	Type         string   `yaml:"type,omitempty" json:"type,omitempty"`
	BotToken     string   `yaml:"bot_token,omitempty" json:"bot_token,omitempty"`
	ChatID       string   `yaml:"chat_id,omitempty" json:"chat_id,omitempty"`
	WebhookURL   string   `yaml:"webhook_url,omitempty" json:"webhook_url,omitempty"`
	SMTPHost     string   `yaml:"smtp_host,omitempty" json:"smtp_host,omitempty"`
	SMTPPort     int      `yaml:"smtp_port,omitempty" json:"smtp_port,omitempty"`
	Username     string   `yaml:"username,omitempty" json:"username,omitempty"`
	Password     string   `yaml:"password,omitempty" json:"password,omitempty"`
	From         string   `yaml:"from,omitempty" json:"from,omitempty"`
	To           []string `yaml:"to,omitempty" json:"to,omitempty"`
	StartTLS     bool     `yaml:"starttls,omitempty" json:"starttls,omitempty"`
}

type Rule struct {
	Events      []string `yaml:"events,omitempty" json:"events,omitempty"`
	Networks    []string `yaml:"networks,omitempty" json:"networks,omitempty"`
	Channels    []string `yaml:"channels,omitempty" json:"channels,omitempty"`
	OnlyUnknown bool     `yaml:"only_unknown,omitempty" json:"only_unknown,omitempty"`
}

// LoadRaw reads the file at path without expanding ${VAR} references or applying defaults.
// It is the form the settings UI edits and writes back.
func LoadRaw(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &c, nil
}

// Effective returns the expanded, defaulted and validated config for raw, leaving raw untouched.
func Effective(raw *Config) (*Config, error) {
	data, err := yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

const savedHeader = "# netmon configuration. Managed by the netmon settings page; hand edits are fine too\n" +
	"# (apply them with \"Reload from file\" or a restart). ${VAR} references come from .env.\n"

// Save writes raw to path atomically (temp file + rename), keeping the file's permissions.
func Save(path string, raw *Config) error {
	var buf bytes.Buffer
	buf.WriteString(savedHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(raw); err != nil {
		return err
	}
	enc.Close()
	mode := os.FileMode(0o640)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("save config: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("save config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
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
			if s.Traceroute && s.Type != "nmap" {
				bad("network %q: traceroute is only available for nmap sources", n.Name)
			}
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
