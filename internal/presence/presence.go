// Package presence learns how long devices are usually silent.
package presence

import (
	"slices"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

const (
	// Window is how long gaps count.
	Window = 7 * 24 * time.Hour
	// MaxGap is the longest silence that counts as sleeping rather than being away.
	MaxGap = 2 * time.Hour

	minGaps      = 5
	minTolerance = 10 * time.Second
)

// Learn finds the usual silence in gaps: at least 5 of them, 70% within 25% of the median.
func Learn(gaps []time.Duration) model.Pattern {
	n := len(gaps)
	if n < minGaps {
		return model.Pattern{}
	}
	s := slices.Sorted(slices.Values(gaps))
	median := (s[(n-1)/2] + s[n/2]) / 2
	tol := max(median/4, minTolerance)
	usual := slices.DeleteFunc(s, func(g time.Duration) bool { return (g - median).Abs() > tol })
	m := len(usual)
	if m*10 < n*7 {
		return model.Pattern{}
	}
	return model.Pattern{Periodic: true, Typical: median, Longest: usual[(m*95+99)/100-1],
		Spread: max(median-usual[0], usual[m-1]-median)}
}

// Limit is how long a device may be silent: the user's override, else 1.5 × its usual longest
// silence, but at least floor (always-on devices: floor).
func Limit(p model.Pattern, override, floor time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if p.Periodic {
		return max(floor, p.Longest*3/2)
	}
	return floor
}
