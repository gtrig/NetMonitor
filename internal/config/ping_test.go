package config

import (
	"strings"
	"testing"
)

func TestPingSource(t *testing.T) {
	parse := func(cidr, sources string) (*Config, error) {
		return Parse([]byte("networks:\n  - name: lan\n    cidr: " + cidr + "\n    sources: [" + sources + "]\n"))
	}
	c, err := parse("10.0.0.0/24", "{type: ping}, {type: services}")
	if err != nil || c.Networks[0].Sources[0].Interval.Duration != DefaultPingInterval {
		t.Fatalf("%+v, %v", c, err)
	}
	for in, want := range map[[2]string]string{
		{"10.0.0.0/24", "{type: ping}, {type: ping}"}:     "only one ping source",
		{"10.0.0.0/24", "{type: ping, interval: 1s}"}:     "at least 2s",
		{"10.0.0.0/24", "{type: ping, ref: x}"}:           "no ref or args",
		{"10.0.0.0/24", "{type: ping, args: [-n]}"}:       "no ref or args",
		{"10.0.0.0/24", "{type: ping, traceroute: true}"}: "traceroute",
		{"10.0.0.0/8", "{type: ping}"}:                    "larger than /16",
	} {
		if _, err := parse(in[0], in[1]); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v", in, err)
		}
	}
}
