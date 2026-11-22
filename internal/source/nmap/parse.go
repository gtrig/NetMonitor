package nmap

import (
	"encoding/xml"
	"fmt"
	"net/netip"

	"github.com/gtrig/netmon/internal/model"
)

type xmlRun struct {
	Hosts []xmlHost `xml:"host"`
}

type xmlHost struct {
	Status struct {
		State string `xml:"state,attr"`
	} `xml:"status"`
	Addresses []struct {
		Addr     string `xml:"addr,attr"`
		AddrType string `xml:"addrtype,attr"`
		Vendor   string `xml:"vendor,attr"`
	} `xml:"address"`
	Hostnames []struct {
		Name string `xml:"name,attr"`
	} `xml:"hostnames>hostname"`
	Hops []struct {
		IP string `xml:"ipaddr,attr"`
	} `xml:"trace>hop"`
}

// ParseXML parses `nmap -oX` output into up hosts and, when traceroute data is present,
// hop-to-hop router links ending at the device.
func ParseXML(data []byte) ([]model.Host, []model.Link, error) {
	var run xmlRun
	if err := xml.Unmarshal(data, &run); err != nil {
		return nil, nil, fmt.Errorf("parse nmap xml: %w", err)
	}
	var hosts []model.Host
	var links []model.Link
	for _, xh := range run.Hosts {
		if xh.Status.State != "up" {
			continue
		}
		var h model.Host
		for _, a := range xh.Addresses {
			switch a.AddrType {
			case "ipv4":
				h.IP, _ = netip.ParseAddr(a.Addr)
			case "mac":
				h.MAC = model.NormalizeMAC(a.Addr)
				h.Vendor = a.Vendor
			}
		}
		if !h.IP.IsValid() {
			continue
		}
		if len(xh.Hostnames) > 0 {
			h.Hostname = xh.Hostnames[0].Name
			h.NameFromDNS = true // nmap names come from PTR lookups
		}
		hosts = append(hosts, h)

		// Hops: all but the last are routers; the last hop is the host itself.
		var prev string
		for i, hop := range xh.Hops {
			last := i == len(xh.Hops)-1
			if last {
				if prev != "" {
					links = append(links, model.Link{A: model.NodeRef{Kind: "router", ID: prev},
						B: model.NodeRef{Kind: "device", ID: model.DeviceKey(h)}})
				}
				break
			}
			if prev != "" {
				links = append(links, model.Link{A: model.NodeRef{Kind: "router", ID: prev},
					B: model.NodeRef{Kind: "router", ID: hop.IP}})
			}
			prev = hop.IP
		}
	}
	return hosts, links, nil
}
