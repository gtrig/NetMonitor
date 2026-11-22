// Package snmp collects ARP, bridge forwarding and LLDP data over SNMP.
package snmp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

// Walker walks an OID subtree.
type Walker interface {
	BulkWalkAll(oid string) ([]gosnmp.SnmpPDU, error)
}

// Source is an SNMP source.
type Source struct {
	name string
	dial func(ctx context.Context) (Walker, func(), error)
}

// New returns a source connecting to t on every collection.
func New(t config.SNMPTarget) *Source {
	return &Source{name: t.Name, dial: func(ctx context.Context) (Walker, func(), error) {
		g := &gosnmp.GoSNMP{
			Target: t.Address, Port: uint16(t.Port), Timeout: 5 * time.Second, Retries: 2,
			MaxRepetitions: 25, Context: ctx,
		}
		if t.Version == "v3" {
			g.Version = gosnmp.Version3
			g.SecurityModel = gosnmp.UserSecurityModel
			usm := &gosnmp.UsmSecurityParameters{UserName: t.Username,
				AuthenticationPassphrase: t.AuthPass, PrivacyPassphrase: t.PrivPass}
			g.MsgFlags = gosnmp.NoAuthNoPriv
			if t.AuthPass != "" {
				g.MsgFlags = gosnmp.AuthNoPriv
				usm.AuthenticationProtocol = authProto(t.AuthProto)
			}
			if t.PrivPass != "" {
				g.MsgFlags = gosnmp.AuthPriv
				usm.PrivacyProtocol = privProto(t.PrivProto)
			}
			g.SecurityParameters = usm
		} else {
			g.Version = gosnmp.Version2c
			g.Community = t.Community
		}
		if err := g.Connect(); err != nil {
			return nil, nil, err
		}
		return g, func() { g.Conn.Close() }, nil
	}}
}

func authProto(s string) gosnmp.SnmpV3AuthProtocol {
	switch strings.ToUpper(s) {
	case "MD5":
		return gosnmp.MD5
	case "SHA256":
		return gosnmp.SHA256
	case "SHA512":
		return gosnmp.SHA512
	}
	return gosnmp.SHA
}

func privProto(s string) gosnmp.SnmpV3PrivProtocol {
	if strings.ToUpper(s) == "DES" {
		return gosnmp.DES
	}
	return gosnmp.AES
}

// NewWithWalker returns a source using w (for tests).
func NewWithWalker(name string, w Walker) *Source {
	return &Source{name: name, dial: func(context.Context) (Walker, func(), error) { return w, func() {}, nil }}
}

func (s *Source) Name() string { return "snmp:" + s.name }

// Collect walks ARP (required) and FDB/ifName/LLDP (best-effort).
func (s *Source) Collect(ctx context.Context, n config.Network) (model.Observations, error) {
	obs := model.Observations{Network: n.Name, Source: s.Name(), At: time.Now().UTC()}
	w, closeFn, err := s.dial(ctx)
	if err != nil {
		return obs, fmt.Errorf("%s: connect: %w", s.Name(), err)
	}
	defer closeFn()

	arp, err := w.BulkWalkAll(oidARP)
	if err != nil {
		return obs, fmt.Errorf("%s: arp walk: %w", s.Name(), err)
	}
	optional := func(oid string) []gosnmp.SnmpPDU {
		pdus, err := w.BulkWalkAll(oid)
		if err != nil {
			slog.Warn("snmp: optional walk failed", "target", s.name, "oid", oid, "err", err)
		}
		return pdus
	}
	local := s.name
	if p := optional(oidSysName); len(p) > 0 {
		if v := string(pduBytes(p[0])); v != "" {
			local = v
		}
	}
	ifNames := parseStringTable(optional(oidIfName))
	ports := parseFDB(optional(oidFDBPort), parseIntTable(optional(oidBasePortIf)), ifNames)
	obs.Links = parseLLDP(local, optional(oidLLDPRemSysName))

	router := model.NodeRef{Kind: "router", ID: local}
	for _, e := range parseARPEntries(arp) {
		h := e.host
		h.Source = s.Name()
		h.Port = ifNames[e.ifIndex]
		if p, ok := ports[h.MAC]; ok {
			h.Port = p
		}
		obs.Hosts = append(obs.Hosts, h)
		obs.Links = append(obs.Links, model.Link{A: router, B: model.NodeRef{Kind: "device", ID: model.DeviceKey(h)}, Label: h.Port})
	}
	for i := range obs.Links {
		obs.Links[i].Source = s.Name()
	}
	return obs, nil
}
