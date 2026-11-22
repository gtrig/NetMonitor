// Package nmap discovers hosts by running `nmap -sn`.
package nmap

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
)

// ExecFunc runs nmap with args and returns stdout.
type ExecFunc func(ctx context.Context, args ...string) ([]byte, error)

// DefaultExec runs the nmap binary from PATH.
func DefaultExec(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "nmap", args...)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return out, fmt.Errorf("%w: %s", err, ee.Stderr)
	}
	return out, err
}

// Source is an nmap host-discovery source.
type Source struct {
	args []string
	exec ExecFunc
}

// New returns an nmap source with extra user args; exec nil means DefaultExec.
func New(args []string, exec ExecFunc) *Source {
	if exec == nil {
		exec = DefaultExec
	}
	return &Source{args: args, exec: exec}
}

func (s *Source) Name() string { return "nmap" }

// BuildArgs returns the full nmap argument list: always host discovery with XML to stdout.
func BuildArgs(user []string, cidr netip.Prefix) []string {
	args := []string{"-sn", "-oX", "-"}
	for _, a := range user {
		if a != "-sn" {
			args = append(args, a)
		}
	}
	return append(args, cidr.String())
}

// Collect runs nmap against the network's CIDR.
func (s *Source) Collect(ctx context.Context, n config.Network) (model.Observations, error) {
	obs := model.Observations{Network: n.Name, Source: s.Name(), At: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, err := s.exec(ctx, BuildArgs(s.args, n.CIDR)...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return obs, fmt.Errorf("nmap not found in PATH: %w", err)
		}
		return obs, fmt.Errorf("nmap: %w", err)
	}
	hosts, links, err := ParseXML(out)
	if err != nil {
		return obs, err
	}
	for i := range hosts {
		hosts[i].Source = s.Name()
	}
	for i := range links {
		links[i].Source = s.Name()
	}
	obs.Hosts, obs.Links = hosts, slices.Clip(links)
	return obs, nil
}
