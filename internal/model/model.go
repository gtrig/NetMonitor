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
	// NameFromDNS marks Hostname as reverse-DNS derived (lower priority than DHCP lease names).
	NameFromDNS bool
	// RTT is the round-trip time from the netmon host (0 = not measured).
	RTT time.Duration
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
	// SeenBy holds, per source name, when that source last reported the device.
	SeenBy map[string]time.Time
	// MergedInto is set (not stored) when this IP-only device was folded into another device;
	// the store moves its label/known flag to that device and deletes this row.
	MergedInto string
}

// MemberRef identifies one stored device row.
type MemberRef struct {
	Network string `json:"network"`
	Key     string `json:"key"`
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

// Port is one open port found by a detailed (services) scan.
type Port struct {
	Proto   string `json:"proto"`
	Port    int    `json:"port"`
	State   string `json:"state"`
	Service string `json:"service,omitempty"`
	Product string `json:"product,omitempty"`
	Version string `json:"version,omitempty"`
	Extra   string `json:"extra,omitempty"`
}

// HostDetails is what a detailed scan found for one address.
type HostDetails struct {
	IP         netip.Addr
	OS         string
	OSAccuracy int
	Ports      []Port
}

// DeviceDetails is the latest detailed-scan result stored for one device row.
type DeviceDetails struct {
	Network    string    `json:"network"`
	Key        string    `json:"key"`
	ScannedAt  time.Time `json:"scannedAt"`
	OS         string    `json:"os,omitempty"`
	OSAccuracy int       `json:"osAccuracy,omitempty"`
	Ports      []Port    `json:"ports"`
}

// Ping is the smoothed round-trip time of one device row.
type Ping struct {
	Network, Key string
	Origin       string // what measured it: a router name, or "netmon"
	RTT          time.Duration
	OK           bool      // false: no recent answer
	At           time.Time // last answer
}

// PingSample is one device's result in a ping run; a negative RTT means no answer.
type PingSample struct {
	Key string
	RTT time.Duration
}
