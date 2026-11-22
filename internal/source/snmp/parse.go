package snmp

import (
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
	"github.com/gtrig/netmon/internal/model"
)

const (
	oidSysName        = ".1.3.6.1.2.1.1.5"
	oidARP            = ".1.3.6.1.2.1.4.22.1.2"   // ipNetToMediaPhysAddress.<ifIndex>.<a.b.c.d>
	oidFDBPort        = ".1.3.6.1.2.1.17.4.3.1.2" // dot1dTpFdbPort.<6 mac octets>
	oidBasePortIf     = ".1.3.6.1.2.1.17.1.4.1.2" // dot1dBasePortIfIndex.<bridgePort>
	oidIfName         = ".1.3.6.1.2.1.31.1.1.1.1" // ifName.<ifIndex>
	oidLLDPRemSysName = ".1.0.8802.1.1.2.1.4.1.1.9"
)

// suffix returns the OID components after base, as ints.
func suffix(name, base string) []int {
	rest := strings.TrimPrefix(strings.TrimPrefix(name, base), ".")
	if rest == name || rest == "" {
		return nil
	}
	parts := strings.Split(rest, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func pduBytes(p gosnmp.SnmpPDU) []byte {
	switch v := p.Value.(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	}
	return nil
}

func pduInt(p gosnmp.SnmpPDU) (int, bool) {
	switch p.Value.(type) {
	case int, uint, int64, uint64, int32, uint32:
		return int(gosnmp.ToBigInt(p.Value).Int64()), true
	}
	return 0, false
}

// arpEntry is one ipNetToMedia row.
type arpEntry struct {
	host    model.Host
	ifIndex int
}

func parseARPEntries(pdus []gosnmp.SnmpPDU) []arpEntry {
	var out []arpEntry
	for _, p := range pdus {
		idx := suffix(p.Name, oidARP)
		if len(idx) != 5 {
			continue
		}
		b := pduBytes(p)
		if len(b) != 6 {
			continue
		}
		mac := model.NormalizeMAC(net.HardwareAddr(b).String())
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		ip := netip.AddrFrom4([4]byte{byte(idx[1]), byte(idx[2]), byte(idx[3]), byte(idx[4])})
		out = append(out, arpEntry{host: model.Host{IP: ip, MAC: mac}, ifIndex: idx[0]})
	}
	return out
}

// parseARP returns hosts from ipNetToMediaPhysAddress PDUs.
func parseARP(pdus []gosnmp.SnmpPDU) []model.Host {
	var out []model.Host
	for _, e := range parseARPEntries(pdus) {
		out = append(out, e.host)
	}
	return out
}

func parseIntTable(pdus []gosnmp.SnmpPDU) map[int]int {
	out := map[int]int{}
	for _, p := range pdus {
		idx := lastIndex(p.Name)
		if v, ok := pduInt(p); ok && idx >= 0 {
			out[idx] = v
		}
	}
	return out
}

func parseStringTable(pdus []gosnmp.SnmpPDU) map[int]string {
	out := map[int]string{}
	for _, p := range pdus {
		if idx := lastIndex(p.Name); idx >= 0 {
			out[idx] = string(pduBytes(p))
		}
	}
	return out
}

func lastIndex(oid string) int {
	i := strings.LastIndex(oid, ".")
	n, err := strconv.Atoi(oid[i+1:])
	if err != nil {
		return -1
	}
	return n
}

// parseFDB returns MAC → interface name from the bridge forwarding table.
func parseFDB(pdus []gosnmp.SnmpPDU, bridgePortToIf map[int]int, ifNames map[int]string) map[string]string {
	out := map[string]string{}
	for _, p := range pdus {
		idx := suffix(p.Name, oidFDBPort)
		port, ok := pduInt(p)
		if len(idx) != 6 || !ok {
			continue
		}
		b := make([]byte, 6)
		for i, v := range idx {
			b[i] = byte(v)
		}
		name := ifNames[bridgePortToIf[port]]
		if name == "" {
			name = "port " + strconv.Itoa(port)
		}
		out[net.HardwareAddr(b).String()] = name
	}
	return out
}

// parseLLDP returns router↔router links from lldpRemSysName PDUs.
func parseLLDP(local string, pdus []gosnmp.SnmpPDU) []model.Link {
	var out []model.Link
	seen := map[string]bool{}
	for _, p := range pdus {
		name := string(pduBytes(p))
		if name == "" || name == local || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, model.Link{A: model.NodeRef{Kind: "router", ID: local}, B: model.NodeRef{Kind: "router", ID: name}})
	}
	return out
}
