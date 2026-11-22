// Package nmap discovers hosts by running `nmap -sn`.
package nmap

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/netip"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/model"
	"github.com/gtrig/netmon/internal/source"
)

// ExecFunc runs nmap with args and returns stdout.
type ExecFunc func(ctx context.Context, args ...string) ([]byte, error)

// Progress is a progress report parsed from nmap's --stats-every output.
type Progress = source.Progress

var (
	taskProgress = regexp.MustCompile(`<taskprogress\b[^>]*\btask="([^"]*)"[^>]*\bpercent="([^"]*)"`)
	taskBegin    = regexp.MustCompile(`<taskbegin\b[^>]*\btask="([^"]*)"`)
)

// ParseProgress reads a <taskprogress task=... percent=.../> line, or a <taskbegin task=.../>
// line, which reports the new task with an unknown percentage (-1).
func ParseProgress(line string) (Progress, bool) {
	if m := taskBegin.FindStringSubmatch(line); m != nil {
		return Progress{Task: strings.TrimSuffix(html.UnescapeString(m[1]), "."), Percent: -1}, true
	}
	m := taskProgress.FindStringSubmatch(line)
	if m == nil {
		return Progress{}, false
	}
	pct, err := strconv.ParseFloat(m[2], 64)
	if err != nil || math.IsNaN(pct) || pct < 0 || pct > 100 {
		return Progress{}, false
	}
	return Progress{Task: strings.TrimSuffix(html.UnescapeString(m[1]), "."), Percent: pct}, true
}

// DefaultExec runs the nmap binary from PATH, reporting progress lines as they arrive.
func DefaultExec(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "nmap", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Without a terminal on stdin nmap prints no progress; scanning still works without one.
	if master, slave, perr := openPTY(); perr == nil {
		attachPTY(cmd, slave)
		defer master.Close()
		defer slave.Close()
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		out.Write(line)
		out.WriteByte('\n')
		if p, ok := ParseProgress(string(line)); ok {
			source.ReportProgress(ctx, p)
		}
	}
	scanErr := sc.Err()
	if scanErr != nil {
		io.Copy(io.Discard, pipe) // let nmap finish writing
	}
	err = cmd.Wait()
	if err != nil && stderr.Len() > 0 {
		return out.Bytes(), fmt.Errorf("%w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	if err == nil {
		err = scanErr
	}
	return out.Bytes(), err
}

// Source is an nmap host-discovery source.
type Source struct {
	args       []string
	traceroute bool
	exec       ExecFunc
}

// New returns an nmap source with extra user args, optionally tracing the route to each host;
// exec nil means DefaultExec.
func New(args []string, traceroute bool, exec ExecFunc) *Source {
	if exec == nil {
		exec = DefaultExec
	}
	return &Source{args: args, traceroute: traceroute, exec: exec}
}

func (s *Source) Name() string { return "nmap" }

// BuildArgs returns the full nmap argument list: always host discovery with XML (and progress
// every 2s) to stdout, plus --traceroute when asked.
func BuildArgs(user []string, traceroute bool, cidr netip.Prefix) []string {
	// -v adds <taskbegin> lines, so phases without percentages (DNS resolution) are reported too.
	args := []string{"-sn", "-v", "-oX", "-", "--stats-every", "2s"}
	if traceroute {
		args = append(args, "--traceroute")
	}
	for _, a := range user {
		if a != "-sn" && a != "-v" && !(traceroute && a == "--traceroute") {
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
	out, err := s.exec(ctx, BuildArgs(s.args, s.traceroute, n.CIDR)...)
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
