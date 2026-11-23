// Package settings converts between the raw config and what the settings UI may see and change.
package settings

import (
	"regexp"
	"slices"

	"github.com/gtrig/netmon/internal/config"
)

// Masked is shown in place of a literal secret; sending it back keeps the stored value.
const Masked = "••••••"

// Editable is the part of the config the settings UI edits.
type Editable struct {
	Workers     *config.Workers     `json:"workers"` // nil: keep what is stored
	Networks    []config.Network    `json:"networks"`
	Routers     []config.Router     `json:"routers"`
	SNMPTargets []config.SNMPTarget `json:"snmp_targets"`
	Channels    []config.Channel    `json:"channels"`
	Rules       []config.Rule       `json:"rules"`
}

var envRef = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// mask keeps empty values and pure ${VAR} references visible and hides everything else.
func mask(v string) string {
	if v == "" || envRef.MatchString(v) {
		return v
	}
	return Masked
}

// keep returns prev when the UI sent no new value (empty or Masked).
func keep(in, prev string) string {
	if in == "" || in == Masked {
		return prev
	}
	return in
}

func routerSecrets(r *config.Router) []*string { return []*string{&r.Password} }
func snmpSecrets(s *config.SNMPTarget) []*string {
	return []*string{&s.Community, &s.AuthPass, &s.PrivPass}
}
func channelSecrets(c *config.Channel) []*string {
	return []*string{&c.BotToken, &c.WebhookURL, &c.Password}
}

// Mask returns the editable sections of raw with secrets hidden and OriginalName set.
func Mask(raw *config.Config) Editable {
	workers := raw.Workers
	e := Editable{
		Workers:     &workers,
		Networks:    nonNil(slices.Clone(raw.Networks)),
		Routers:     nonNil(slices.Clone(raw.Routers)),
		SNMPTargets: nonNil(slices.Clone(raw.SNMPTargets)),
		Channels:    nonNil(slices.Clone(raw.Channels)),
		Rules:       nonNil(slices.Clone(raw.Rules)),
	}
	for i := range e.Networks {
		e.Networks[i].OriginalName = e.Networks[i].Name
	}
	for i := range e.Routers {
		e.Routers[i].OriginalName = e.Routers[i].Name
		for _, p := range routerSecrets(&e.Routers[i]) {
			*p = mask(*p)
		}
	}
	for i := range e.SNMPTargets {
		e.SNMPTargets[i].OriginalName = e.SNMPTargets[i].Name
		for _, p := range snmpSecrets(&e.SNMPTargets[i]) {
			*p = mask(*p)
		}
	}
	for i := range e.Channels {
		e.Channels[i].OriginalName = e.Channels[i].Name
		for _, p := range channelSecrets(&e.Channels[i]) {
			*p = mask(*p)
		}
	}
	return e
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// merge restores secrets for items whose OriginalName names an existing item. Only the first
// item claiming an original name inherits its secrets; new items inherit nothing.
func merge[T any](in, prev []T, name, origName func(*T) string, secrets func(*T) []*string, clear func(*T)) []T {
	out := slices.Clone(in)
	byName := map[string]*T{}
	for i := range prev {
		p := &prev[i]
		byName[name(p)] = p
	}
	claimed := map[string]bool{}
	for i := range out {
		item := &out[i]
		orig := origName(item)
		var old *T
		if orig != "" && !claimed[orig] {
			old = byName[orig]
			claimed[orig] = true
		}
		news := secrets(item)
		var olds []*string
		if old != nil {
			olds = secrets(old)
		}
		for j, p := range news {
			prevVal := ""
			if olds != nil {
				prevVal = *olds[j]
			}
			*p = keep(*p, prevVal)
		}
		clear(item)
	}
	return out
}

// Apply returns a copy of raw with the editable sections replaced by e, keeping stored
// secrets the UI did not change. raw is not modified.
func Apply(raw *config.Config, e Editable) *config.Config {
	out := *raw
	if e.Workers != nil {
		out.Workers = *e.Workers
	}
	out.Networks = slices.Clone(e.Networks)
	for i := range out.Networks {
		out.Networks[i].OriginalName = ""
	}
	out.Rules = slices.Clone(e.Rules)
	out.Routers = merge(e.Routers, raw.Routers,
		func(r *config.Router) string { return r.Name }, func(r *config.Router) string { return r.OriginalName }, routerSecrets,
		func(r *config.Router) { r.OriginalName = "" })
	out.SNMPTargets = merge(e.SNMPTargets, raw.SNMPTargets,
		func(s *config.SNMPTarget) string { return s.Name }, func(s *config.SNMPTarget) string { return s.OriginalName }, snmpSecrets,
		func(s *config.SNMPTarget) { s.OriginalName = "" })
	out.Channels = merge(e.Channels, raw.Channels,
		func(c *config.Channel) string { return c.Name }, func(c *config.Channel) string { return c.OriginalName }, channelSecrets,
		func(c *config.Channel) { c.OriginalName = "" })
	return &out
}

// NetworkRenames returns old name → new name for every network in e that was renamed from a
// network of raw. Only the first item claiming an original name counts.
func NetworkRenames(raw *config.Config, e Editable) map[string]string {
	existing := map[string]bool{}
	for _, n := range raw.Networks {
		existing[n.Name] = true
	}
	out := map[string]string{}
	claimed := map[string]bool{}
	for _, n := range e.Networks {
		o := n.OriginalName
		if o == "" || claimed[o] || !existing[o] {
			continue
		}
		claimed[o] = true
		if o != n.Name {
			out[o] = n.Name
		}
	}
	return out
}
