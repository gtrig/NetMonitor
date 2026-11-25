//go:build !linux

package ping

import (
	"net/netip"

	"github.com/gtrig/netmon/internal/source"
)

func neighbors() map[netip.Addr]source.Neighbor { return nil }
