package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/store"
)

// deviceRow is a device with its machine id (0 = not grouped) for the devices page.
type deviceRow struct {
	model.Device
	Machine int64
}

// groupRows attaches machine ids and moves the members of each machine right after its first member.
func groupRows(devs []model.Device, machines map[model.MemberRef]int64) []deviceRow {
	rows := make([]deviceRow, 0, len(devs))
	byMachine := map[int64][]deviceRow{}
	var order []any // deviceRow or machine id
	for _, d := range devs {
		r := deviceRow{Device: d, Machine: machines[model.MemberRef{Network: d.Network, Key: d.Key}]}
		if r.Machine == 0 {
			order = append(order, r)
			continue
		}
		if _, seen := byMachine[r.Machine]; !seen {
			order = append(order, r.Machine)
		}
		byMachine[r.Machine] = append(byMachine[r.Machine], r)
	}
	for _, o := range order {
		switch v := o.(type) {
		case deviceRow:
			rows = append(rows, v)
		case int64:
			rows = append(rows, byMachine[v]...)
		}
	}
	return rows
}

func (s *server) configured(name string) bool {
	return slices.ContainsFunc(s.rt().Config.Networks, func(n config.Network) bool { return n.Name == name })
}

func (s *server) apiDataNetworks(w http.ResponseWriter, r *http.Request) {
	nd, err := s.Store.DataNetworks(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	type item struct {
		store.NetworkData
		Configured bool `json:"configured"`
	}
	out := []item{}
	for _, n := range nd {
		out = append(out, item{n, s.configured(n.Name)})
	}
	writeJSON(w, out)
}

// staleTarget checks that {name} is editable stale data; it writes the error response otherwise.
func (s *server) staleTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.canEdit() {
		http.Error(w, "read-only until auth is configured", http.StatusForbidden)
		return "", false
	}
	name := r.PathValue("name")
	if s.configured(name) {
		http.Error(w, "network is still configured; rename or remove it on the settings page", http.StatusConflict)
		return "", false
	}
	return name, true
}

func (s *server) apiMergeNetworkData(w http.ResponseWriter, r *http.Request) {
	name, ok := s.staleTarget(w, r)
	if !ok {
		return
	}
	var body struct {
		Into string `json:"into"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !s.configured(body.Into) {
		http.Error(w, "into must name a configured network", http.StatusBadRequest)
		return
	}
	if err := s.Store.MergeStaleNetwork(r.Context(), name, body.Into); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) apiDeleteNetworkData(w http.ResponseWriter, r *http.Request) {
	name, ok := s.staleTarget(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteNetworkData(r.Context(), name); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) apiDeleteDevice(w http.ResponseWriter, r *http.Request) {
	if !s.canEdit() {
		http.Error(w, "read-only until auth is configured", http.StatusForbidden)
		return
	}
	err := s.Store.DeleteDevice(r.Context(), r.URL.Query().Get("network"), r.PathValue("key"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "device not found", http.StatusNotFound)
	case err != nil:
		serverError(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) apiMergeMachine(w http.ResponseWriter, r *http.Request) {
	if !s.canEdit() {
		http.Error(w, "read-only until auth is configured", http.StatusForbidden)
		return
	}
	var body struct {
		Members []model.MemberRef `json:"members"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	id, err := s.Store.MergeMachine(r.Context(), body.Members)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]int64{"id": id})
}

func (s *server) apiSplitMachine(w http.ResponseWriter, r *http.Request) {
	if !s.canEdit() {
		http.Error(w, "read-only until auth is configured", http.StatusForbidden)
		return
	}
	var ref model.MemberRef
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&ref); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := s.Store.SplitMember(r.Context(), ref); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
