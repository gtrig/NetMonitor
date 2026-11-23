package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

// option is a choice of a filter.
type option struct{ Value, Label string }

var runKinds = []option{{model.RunPresence, "Quick scans"}, {model.RunDetails, "Network detailed scans"},
	{model.RunDevice, "Device detailed scans"}, {model.RunPing, "Pings"}}

var runOutcomes = []option{{model.OutcomeOK, "OK"}, {model.OutcomeFailed, "Failed"}, {model.OutcomeCancelled, "Cancelled"},
	{model.OutcomeSkipped, "Skipped"}, {model.OutcomeInterrupted, "Interrupted"}}

// jobsView is what the Jobs page shows.
type jobsView struct {
	Kinds, Outcomes []option
	Filter          store.JobRunFilter
	Runs            []jobRow
	Filtered        bool
	Newest, Older   string // links; empty when there is none
}

// jobRow is a finished run; Name and Link are set when it is of a device that is still listed.
type jobRow struct {
	model.JobRun
	KindLabel, OutcomeLabel string
	Name, Link              string
}

func label(opts []option, v string) string {
	if i := slices.IndexFunc(opts, func(o option) bool { return o.Value == v }); i >= 0 {
		return opts[i].Label
	}
	return v
}

func (s *server) jobsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.JobRunFilter{Network: q.Get("network"), Kind: q.Get("kind"), Outcome: q.Get("outcome"), Limit: 100}
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && v > 0 {
		f.Before = v
	}
	nets, err := s.networks(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	runs, err := s.Store.JobRuns(r.Context(), f)
	if err != nil {
		serverError(w, err)
		return
	}
	devs, err := s.Store.Devices(r.Context(), "")
	if err != nil {
		serverError(w, err)
		return
	}
	byRef := map[model.MemberRef]model.Device{}
	for _, d := range devs {
		byRef[model.MemberRef{Network: d.Network, Key: d.Key}] = d
	}
	v := &jobsView{Kinds: runKinds, Outcomes: runOutcomes, Filter: f, Filtered: f.Network != "" || f.Kind != "" || f.Outcome != ""}
	for _, run := range runs {
		row := jobRow{JobRun: run, KindLabel: strings.TrimSuffix(label(runKinds, run.Kind), "s"), OutcomeLabel: label(runOutcomes, run.Outcome)}
		if d, ok := byRef[model.MemberRef{Network: run.Network, Key: run.Key}]; ok && run.Key != "" {
			row.Link = "/device?" + url.Values{"network": {run.Network}, "key": {run.Key}}.Encode()
			row.Name = d.Label
			if row.Name == "" {
				row.Name = d.Hostname
			}
		}
		v.Runs = append(v.Runs, row)
	}
	link := func(before int64) string {
		p := url.Values{}
		for k, val := range map[string]string{"network": f.Network, "kind": f.Kind, "outcome": f.Outcome} {
			if val != "" {
				p.Set(k, val)
			}
		}
		if before > 0 {
			p.Set("before", strconv.FormatInt(before, 10))
		}
		return "/jobs?" + p.Encode()
	}
	if f.Before > 0 {
		v.Newest = link(0)
	}
	if len(runs) == f.Limit {
		v.Older = link(runs[len(runs)-1].ID)
	}
	s.render(w, "jobs", pageData{Title: "Jobs", Networks: nets, Network: f.Network, Jobs: v})
}
