package web

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/wol"
)

// apiWake sends a Wake-on-LAN packet to a stored device. The address comes from the store and
// the network from the config, so a client cannot aim packets elsewhere.
func (s *server) apiWake(w http.ResponseWriter, r *http.Request) {
	var ref model.MemberRef
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&ref); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	devs, err := s.Store.Devices(r.Context(), ref.Network)
	if err != nil {
		serverError(w, err)
		return
	}
	i := slices.IndexFunc(devs, func(d model.Device) bool { return d.Network == ref.Network && d.Key == ref.Key })
	if i < 0 {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	if devs[i].MAC == "" {
		http.Error(w, "the device's MAC address is not known", http.StatusConflict)
		return
	}
	nets := s.rt().Config.Networks
	n := slices.IndexFunc(nets, func(n config.Network) bool { return n.Name == ref.Network })
	if n < 0 {
		http.Error(w, "network is not configured", http.StatusConflict)
		return
	}
	send := s.Wake
	if send == nil {
		send = wol.Send
	}
	if err := send(devs[i].MAC, nets[n].CIDR); err != nil {
		http.Error(w, "wake packet not sent: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
