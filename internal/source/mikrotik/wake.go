package mikrotik

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/config"
)

// ErrWakeDenied is returned when the router user may not run /tool/wol.
var ErrWakeDenied = errors.New(`router user needs the "test" policy to send wake packets`)

// Wake has router r send a Wake-on-LAN packet for mac on its interface in network, which also
// reaches networks netmon is not attached to. It uses its own, short-lived API connection.
func Wake(ctx context.Context, r config.Router, mac string, network netip.Prefix) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	run := &apiRunner{cfg: r}
	defer func() {
		if run.client != nil {
			run.client.Close()
		}
	}()
	return wake(ctx, run, r.Name, mac, network)
}

func wake(ctx context.Context, run commandRunner, router, mac string, network netip.Prefix) error {
	rows, err := run.Run(ctx, "/ip/address/print")
	if err != nil {
		return fmt.Errorf("router %s: %w", router, err)
	}
	iface := lanInterface(rows, network)
	if iface == "" {
		return fmt.Errorf("router %s has no address in %s", router, network)
	}
	if _, err := run.Run(ctx, "/tool/wol", "=interface="+iface, "=mac="+mac); err != nil {
		if strings.Contains(err.Error(), "not enough permissions") {
			err = ErrWakeDenied
		}
		return fmt.Errorf("router %s: %w", router, err)
	}
	return nil
}

// lanInterface is the interface whose subnet is network, or else the most specific one that
// overlaps it. Single-host (point-to-point) addresses do not count.
func lanInterface(rows []map[string]string, network netip.Prefix) string {
	network = network.Masked()
	best, bits := "", -1
	for _, r := range rows {
		p, err := netip.ParsePrefix(r["address"])
		if err != nil || p.IsSingleIP() || r["disabled"] == "true" || r["invalid"] == "true" || !p.Overlaps(network) {
			continue
		}
		iface := r["actual-interface"] // the bridge, when the address is on one of its ports
		if iface == "" {
			iface = r["interface"]
		}
		if p.Masked() == network {
			return iface
		}
		if p.Bits() > bits {
			best, bits = iface, p.Bits()
		}
	}
	return best
}
