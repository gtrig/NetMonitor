// Package enrich fills in vendor and hostname details for observed hosts.
package enrich

import (
	"bufio"
	"context"
	"os"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// OUI maps MAC prefixes to vendor names.
type OUI struct{ prefixes map[string]string }

// LoadOUI reads a file in nmap-mac-prefixes format ("000C42 Vendor Name" per line).
func LoadOUI(path string) (*OUI, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	o := &OUI{prefixes: map[string]string{}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		prefix, name, ok := strings.Cut(line, " ")
		if !ok || len(prefix) != 6 {
			continue
		}
		o.prefixes[strings.ToUpper(prefix)] = strings.TrimSpace(name)
	}
	return o, sc.Err()
}

// Vendor returns the vendor for mac, or "" if unknown. Safe on a nil receiver.
func (o *OUI) Vendor(mac string) string {
	if o == nil {
		return ""
	}
	m := model.NormalizeMAC(mac)
	if m == "" {
		return ""
	}
	return o.prefixes[strings.ToUpper(strings.ReplaceAll(m[:8], ":", ""))]
}

// Enrich fills empty Vendor (from the OUI table) and Hostname (via reverse DNS) fields.
// Either oui or r may be nil.
func Enrich(obs *model.Observations, oui *OUI, r *Resolver) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := range obs.Hosts {
		h := &obs.Hosts[i]
		if h.Vendor == "" {
			h.Vendor = oui.Vendor(h.MAC)
		}
		if h.Hostname == "" && r != nil && h.IP.IsValid() {
			h.Hostname = r.Name(ctx, h.IP)
			h.NameFromDNS = h.Hostname != ""
		}
	}
}
