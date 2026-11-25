package web

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

// deviceView is what the device details page shows.
type deviceView struct {
	deviceRow
	Name   string
	Links  []string
	Others []deviceRow // the other addresses of the same machine
}

// Pattern describes how long the device is usually silent, and what that means.
func (r deviceRow) Pattern() string {
	p := r.Presence
	if p == nil {
		return ""
	}
	out := "no regular pattern"
	if p.Pattern.Periodic {
		out = "silent for about " + roundDuration(p.Pattern.Typical) + " at a time (±" + roundDuration(p.Pattern.Spread) + ")"
	}
	return out + ", offline after " + roundDuration(p.Limit)
}

func roundDuration(d time.Duration) string {
	if d > 10*time.Minute && d%time.Minute != 0 {
		d = d.Round(30 * time.Second)
	}
	return config.FormatDuration(d.Round(time.Second))
}

// Override is the user's own limit, or "".
func (r deviceRow) Override() string {
	if r.OfflineAfter == 0 {
		return ""
	}
	return config.FormatDuration(r.OfflineAfter)
}

// webLinks returns the web interfaces among the scanned ports, or http://<ip> when there are none.
// The map panel has its own copy of these rules (webLinks in map.js).
func webLinks(ip netip.Addr, ports []model.Port) []string {
	if !ip.IsValid() {
		return nil
	}
	host := ip.String()
	if ip.Is6() {
		host = "[" + host + "]"
	}
	var out []string
	for _, p := range ports {
		svc := strings.ToLower(p.Service)
		if p.Proto != "tcp" || !(strings.Contains(svc, "http") || slices.Contains([]int{80, 443, 8080, 8443}, p.Port)) {
			continue
		}
		scheme, def := "http", 80
		if strings.Contains(svc, "https") || strings.Contains(svc, "ssl") || strings.Contains(svc, "tls") ||
			slices.Contains([]int{443, 8443, 9443}, p.Port) {
			scheme, def = "https", 443
		}
		u := scheme + "://" + host
		if p.Port != def {
			u = scheme + "://" + net.JoinHostPort(ip.String(), strconv.Itoa(p.Port))
		}
		if u += "/"; !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	if out == nil {
		out = []string{fmt.Sprintf("http://%s/", host)}
	}
	return out
}

func (s *server) devicePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	self := model.MemberRef{Network: q.Get("network"), Key: q.Get("key")}
	devs, err := s.Store.Devices(r.Context(), "")
	if err != nil {
		serverError(w, err)
		return
	}
	machines, err := s.Store.Machines(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	details, err := s.Store.Details(r.Context(), "")
	if err != nil {
		serverError(w, err)
		return
	}
	var v *deviceView
	refs := []model.MemberRef{self}
	var others []deviceRow
	for _, row := range s.withPresence(groupRows(devs, machines, details), devs) {
		ref := model.MemberRef{Network: row.Network, Key: row.Key}
		switch {
		case ref == self:
			v = &deviceView{deviceRow: row}
		case machines[self] != 0 && row.Machine == machines[self]:
			refs = append(refs, ref)
			others = append(others, row)
		}
	}
	if v == nil {
		http.Error(w, "This device is not in the list any more. It may have been forgotten or merged; see /devices.", http.StatusNotFound)
		return
	}
	v.Others = others
	v.Name = v.Label
	if v.Name == "" {
		v.Name = v.Hostname
	}
	if v.Name == "" && v.IP.IsValid() {
		v.Name = v.IP.String()
	}
	if v.Name == "" {
		v.Name = v.Key
	}
	var ports []model.Port
	if v.Details != nil {
		ports = v.Details.Ports
	}
	v.Links = webLinks(v.IP, ports)
	f := eventFilter(r)
	f.Network, f.Devices = "", refs
	evs, err := s.Store.Events(r.Context(), f)
	if err != nil {
		serverError(w, err)
		return
	}
	var older int64
	if len(evs) == f.Limit {
		older = evs[len(evs)-1].ID
	}
	s.render(w, "device", pageData{Title: v.Name, Device: v, Events: evs, Older: older, CanEdit: s.canEdit()})
}
