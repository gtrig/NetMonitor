// Package model holds the types shared between sources, storage, diffing and the web UI.
package model

import (
	"net"
	"net/netip"
	"strings"
	"time"
)

// Host is one host as reported by a source.
type Host struct {
	IP       netip.Addr
	MAC      string
	Hostname string
	Vendor   string
	Port     string // bridge/interface the host was seen on, if known
	Source   string
}

// NodeRef identifies a node in the topology. Kind is "router" or "device".
type NodeRef struct {
	Kind string
	ID   string
}

// Link is an edge between two topology nodes.
type Link struct {
	A, B   NodeRef
	Label  string
	Source string
}

// Observations is the result of one collection by one source for one network.
type Observations struct {
	Network string
	Source  string
	At      time.Time
	Hosts   []Host
	Links   []Link
}

// Device is a tracked device as stored.
type Device struct {
	Key       string
	Network   string
	MAC       string
	IP        netip.Addr
	Hostname  string
	Vendor    string
	Label     string
	Port      string
	Known     bool
	Online    bool
	FirstSeen time.Time
	LastSeen  time.Time
	Sources   []string
	// PrevKey is set (not stored) when the device's key changed in this update,
	// e.g. an IP-only device whose MAC became known; the store renames the row.
	PrevKey string
}

// Event is a detected change.
type Event struct {
	ID        int64
	Type      string
	Network   string
	DeviceKey string
	Message   string
	At        time.Time
	Old, New  string
}

// NormalizeMAC returns mac as lowercase colon-separated hex, or "" if invalid.
func NormalizeMAC(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return ""
	}
	return hw.String()
}

// DeviceKey returns "mac:<mac>" when the host has a valid MAC, else "ip:<addr>".
func DeviceKey(h Host) string {
	if m := NormalizeMAC(h.MAC); m != "" {
		return "mac:" + m
	}
	return "ip:" + h.IP.String()
}
