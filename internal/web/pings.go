package web

import (
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

const pingPageSize = 50

// pingRanges are the graph's ranges and their bucket sizes (0: every run).
var pingRanges = map[string]struct{ span, bucket time.Duration }{
	"1h":  {time.Hour, 0},
	"24h": {24 * time.Hour, 0},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
	"30d": {30 * 24 * time.Hour, 2 * time.Hour},
}

// pingPoint is one run or one bucket of runs; Ms (the median) is null when none was answered.
type pingPoint struct {
	At     time.Time `json:"at"`
	Ms     *float64  `json:"ms"`
	Min    *float64  `json:"min,omitempty"`
	Max    *float64  `json:"max,omitempty"`
	Runs   int       `json:"runs"`
	Missed int       `json:"missed"`
	Origin string    `json:"origin"`
}

type pingSummary struct {
	Runs     int      `json:"runs"`
	Answered float64  `json:"answered"` // percent
	Median   *float64 `json:"median"`
	Min      *float64 `json:"min"`
	Max      *float64 `json:"max"`
}

func millis(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// answered returns the sorted times of the answered runs.
func answered(ps []model.PingPoint) []float64 {
	var ms []float64
	for _, p := range ps {
		if p.OK {
			ms = append(ms, millis(p.RTT))
		}
	}
	slices.Sort(ms)
	return ms
}

func stats(ms []float64) (median, lo, hi *float64) {
	n := len(ms)
	if n == 0 {
		return nil, nil, nil
	}
	m := (ms[(n-1)/2] + ms[n/2]) / 2
	return &m, &ms[0], &ms[n-1]
}

// bucketPings groups runs (oldest first) by time and origin; size 0 keeps every run.
func bucketPings(ps []model.PingPoint, size time.Duration) []pingPoint {
	out := []pingPoint{}
	for i := 0; i < len(ps); {
		j := i + 1
		at := ps[i].At
		if size > 0 {
			at = at.Truncate(size)
			for j < len(ps) && ps[j].At.Truncate(size).Equal(at) && ps[j].Origin == ps[i].Origin {
				j++
			}
			at = at.Add(size / 2)
		}
		ms := answered(ps[i:j])
		p := pingPoint{At: at, Runs: j - i, Missed: j - i - len(ms), Origin: ps[i].Origin}
		p.Ms, p.Min, p.Max = stats(ms)
		if size == 0 {
			p.Min, p.Max = nil, nil
		}
		out = append(out, p)
		i = j
	}
	return out
}

func summarizePings(ps []model.PingPoint) pingSummary {
	ms := answered(ps)
	s := pingSummary{Runs: len(ps)}
	if len(ps) > 0 {
		s.Answered = 100 * float64(len(ms)) / float64(len(ps))
	}
	s.Median, s.Min, s.Max = stats(ms)
	return s
}

func (s *server) apiPings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("range")
	if name == "" {
		name = "24h"
	}
	rg, ok := pingRanges[name]
	if !ok {
		http.Error(w, "unknown range", http.StatusBadRequest)
		return
	}
	to := time.Now().UTC()
	from := to.Add(-rg.span)
	ps, err := s.Store.PingHistory(r.Context(), q.Get("network"), q.Get("key"), from)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"from": from, "to": to, "bucket": rg.bucket.Seconds(),
		"points": bucketPings(ps, rg.bucket), "summary": summarizePings(ps)})
}

func (s *server) apiPingList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var cur store.PingCursor
	if b := q.Get("before"); b != "" {
		if n, err := fmt.Sscanf(b, "%d-%d", &cur.At, &cur.ID); n != 2 || err != nil {
			http.Error(w, "bad cursor", http.StatusBadRequest)
			return
		}
	}
	ps, next, err := s.Store.PingPage(r.Context(), q.Get("network"), q.Get("key"), cur, pingPageSize)
	if err != nil {
		serverError(w, err)
		return
	}
	nextText := ""
	if next != (store.PingCursor{}) {
		nextText = fmt.Sprintf("%d-%d", next.At, next.ID)
	}
	writeJSON(w, map[string]any{"rows": bucketPings(ps, 0), "next": nextText})
}
