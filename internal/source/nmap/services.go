package nmap

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// ServicesTimeout bounds one detailed scan.
const ServicesTimeout = 3 * time.Hour

// Services runs detailed scans: open TCP ports, service versions and an OS guess.
type Services struct {
	args []string
	exec ExecFunc
}

// NewServices returns a detailed scanner with extra user args; exec nil means DefaultExec.
func NewServices(args []string, exec ExecFunc) *Services {
	if exec == nil {
		exec = DefaultExec
	}
	return &Services{args: args, exec: exec}
}

func (s *Services) Name() string { return "services" }

var outputFlags = map[string]bool{"-oX": true, "-oN": true, "-oG": true, "-oA": true, "-oS": true}

// ServiceArgs returns the nmap arguments of a detailed scan of targets. The targets are known to
// be up (-Pn). User output flags are dropped so the XML on stdout stays parseable.
func ServiceArgs(user []string, targets []netip.Addr) []string {
	args := []string{"-sV", "-O", "-Pn", "-v", "-oX", "-", "--stats-every", "2s"}
	for i := 0; i < len(user); i++ {
		if outputFlags[user[i]] {
			i++ // skip the file name too
			continue
		}
		args = append(args, user[i])
	}
	for _, t := range targets {
		args = append(args, t.String())
	}
	return args
}

// Scan runs a detailed scan of targets. No targets means nothing to do.
func (s *Services) Scan(ctx context.Context, targets []netip.Addr) ([]model.HostDetails, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, ServicesTimeout)
	defer cancel()
	out, err := s.exec(ctx, ServiceArgs(s.args, targets)...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("nmap not found in PATH: %w", err)
		}
		return nil, fmt.Errorf("nmap: %w", err)
	}
	return ParseServicesXML(out)
}

type xmlServicesRun struct {
	Hosts []struct {
		Status struct {
			State string `xml:"state,attr"`
		} `xml:"status"`
		Addresses []struct {
			Addr     string `xml:"addr,attr"`
			AddrType string `xml:"addrtype,attr"`
		} `xml:"address"`
		Ports []struct {
			Protocol string `xml:"protocol,attr"`
			PortID   string `xml:"portid,attr"`
			State    struct {
				State string `xml:"state,attr"`
			} `xml:"state"`
			Service struct {
				Name      string `xml:"name,attr"`
				Product   string `xml:"product,attr"`
				Version   string `xml:"version,attr"`
				ExtraInfo string `xml:"extrainfo,attr"`
			} `xml:"service"`
		} `xml:"ports>port"`
		OSMatches []struct {
			Name     string `xml:"name,attr"`
			Accuracy string `xml:"accuracy,attr"`
		} `xml:"os>osmatch"`
	} `xml:"host"`
}

// ParseServicesXML parses the XML of a detailed scan: up hosts, their open ports, and the
// best OS match.
func ParseServicesXML(data []byte) ([]model.HostDetails, error) {
	var run xmlServicesRun
	if err := xml.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("parse nmap xml: %w", err)
	}
	var out []model.HostDetails
	for _, h := range run.Hosts {
		if h.Status.State != "up" {
			continue
		}
		var d model.HostDetails
		for _, a := range h.Addresses {
			if a.AddrType == "ipv4" {
				d.IP, _ = netip.ParseAddr(a.Addr)
			}
		}
		if !d.IP.IsValid() {
			continue
		}
		d.Ports = []model.Port{}
		for _, p := range h.Ports {
			if p.State.State != "open" {
				continue
			}
			n, err := strconv.Atoi(p.PortID)
			if err != nil {
				continue
			}
			d.Ports = append(d.Ports, model.Port{Proto: p.Protocol, Port: n, State: p.State.State, Service: p.Service.Name,
				Product: p.Service.Product, Version: p.Service.Version, Extra: p.Service.ExtraInfo})
		}
		if len(h.OSMatches) > 0 { // nmap lists matches best first
			d.OS = h.OSMatches[0].Name
			d.OSAccuracy, _ = strconv.Atoi(h.OSMatches[0].Accuracy)
		}
		out = append(out, d)
	}
	return out, nil
}
