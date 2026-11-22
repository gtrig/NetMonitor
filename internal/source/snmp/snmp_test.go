package snmp

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/gosnmp/gosnmp"
	"github.com/gtrig/netmon/internal/config"
)

type fakeWalker struct {
	data map[string][]gosnmp.SnmpPDU
	errs map[string]error
}

func (f fakeWalker) BulkWalkAll(oid string) ([]gosnmp.SnmpPDU, error) {
	if err := f.errs[oid]; err != nil {
		return nil, err
	}
	return f.data[oid], nil
}

func oct(oid string, v []byte) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.OctetString, Value: v}
}
func num(oid string, v int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Integer, Value: v}
}

func walkData() map[string][]gosnmp.SnmpPDU {
	return map[string][]gosnmp.SnmpPDU{
		oidSysName: {oct(oidSysName+".0", []byte("core"))},
		oidARP: {
			oct(oidARP+".7.192.168.88.10", []byte{0xaa, 0, 0, 0, 0, 0x10}),
			oct(oidARP+".7.192.168.88.11", []byte{0xaa, 0, 0, 0, 0, 0x11}),
			oct(oidARP+".7.192.168.88.12", []byte{}), // incomplete
		},
		oidFDBPort:        {num(oidFDBPort+".170.0.0.0.0.16", 2)},
		oidBasePortIf:     {num(oidBasePortIf+".2", 3)},
		oidIfName:         {oct(oidIfName+".3", []byte("ether2"))},
		oidLLDPRemSysName: {oct(oidLLDPRemSysName+".0.5.1", []byte("ap-attic"))},
	}
}

var lan = config.Network{Name: "lan", CIDR: netip.MustParsePrefix("192.168.88.0/24")}

func TestParseARP(t *testing.T) {
	hosts := parseARP(walkData()[oidARP])
	if len(hosts) != 2 || hosts[0].IP.String() != "192.168.88.10" || hosts[0].MAC != "aa:00:00:00:00:10" {
		t.Fatalf("hosts = %+v", hosts)
	}
}

func TestParseFDB(t *testing.T) {
	d := walkData()
	ports := parseFDB(d[oidFDBPort], parseIntTable(d[oidBasePortIf]), parseStringTable(d[oidIfName]))
	if ports["aa:00:00:00:00:10"] != "ether2" {
		t.Fatalf("ports = %v", ports)
	}
}

func TestParseLLDP(t *testing.T) {
	links := parseLLDP("core", walkData()[oidLLDPRemSysName])
	if len(links) != 1 || links[0].A.ID != "core" || links[0].B.ID != "ap-attic" || links[0].B.Kind != "router" {
		t.Fatalf("links = %+v", links)
	}
}

func TestCollectWithoutLLDP(t *testing.T) {
	w := fakeWalker{data: walkData(), errs: map[string]error{oidLLDPRemSysName: errors.New("no such object"), oidFDBPort: errors.New("nope")}}
	o, err := NewWithWalker("core-snmp", w).Collect(context.Background(), lan)
	if err != nil || len(o.Hosts) != 2 || o.Source != "snmp:core-snmp" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

func TestCollectPortLinks(t *testing.T) {
	o, err := NewWithWalker("core-snmp", fakeWalker{data: walkData()}).Collect(context.Background(), lan)
	if err != nil {
		t.Fatal(err)
	}
	if o.Hosts[0].Port != "ether2" {
		t.Fatalf("host = %+v", o.Hosts[0])
	}
	found := false
	for _, l := range o.Links {
		if l.A.ID == "core" && l.B.ID == "mac:aa:00:00:00:00:10" && l.Label == "ether2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("links = %+v", o.Links)
	}
}

func TestCollectARPErrorFails(t *testing.T) {
	w := fakeWalker{data: walkData(), errs: map[string]error{oidARP: errors.New("timeout")}}
	if _, err := NewWithWalker("x", w).Collect(context.Background(), lan); err == nil {
		t.Fatal("expected error")
	}
}
