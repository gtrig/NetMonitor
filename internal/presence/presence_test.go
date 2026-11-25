package presence

import (
	"testing"
	"time"
)

func mins(v ...float64) []time.Duration {
	var out []time.Duration
	for _, m := range v {
		out = append(out, time.Duration(m*float64(time.Minute)))
	}
	return out
}

func TestLearn(t *testing.T) {
	for name, c := range map[string]struct {
		gaps  []time.Duration
		limit time.Duration
	}{
		"regular":               {mins(10, 10, 9, 11, 10), 990 * time.Second},
		"too few":               {mins(10, 10, 10, 10), 30 * time.Second},
		"random":                {mins(1, 7, 30, 55, 110, 3), 30 * time.Second},
		"outliers are left out": {mins(10, 10, 10, 10, 10, 10, 10, 110, 100), 15 * time.Minute},
		"too many outliers":     {mins(10, 10, 10, 60, 100), 30 * time.Second},
		"short gaps":            {mins(0.5, 0.6, 0.5, 0.6, 0.5), 54 * time.Second},
		"none":                  {nil, 30 * time.Second},
	} {
		if got := Limit(Learn(c.gaps), 0, 30*time.Second); got != c.limit {
			t.Errorf("%s: limit %v, want %v", name, got, c.limit)
		}
	}
	p := Learn(mins(10, 10, 9, 11, 10))
	if !p.Periodic || p.Typical != 10*time.Minute || p.Spread != time.Minute || p.Longest != 11*time.Minute {
		t.Errorf("%+v", p)
	}
	if got := Limit(p, time.Minute, 2*time.Minute); got != time.Minute {
		t.Errorf("override: %v", got)
	}
	if got := Limit(p, 0, time.Hour); got != time.Hour {
		t.Errorf("floor: %v", got)
	}
}
