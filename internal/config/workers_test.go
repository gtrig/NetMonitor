package config

import "testing"

func TestWorkers(t *testing.T) {
	for in, want := range map[string]Workers{
		"":                                 {4, 2},
		"workers: {scans: 2}":              {2, 1},
		"workers: {scans: 8, detailed: 7}": {8, 7},
	} {
		c, err := Parse([]byte(in + "\nrouters: []\n"))
		if err != nil || c.Workers != want {
			t.Errorf("%q: %+v, %v", in, c, err)
		}
	}
	for _, in := range []string{"{scans: 1}", "{scans: 65}", "{scans: 3, detailed: 3}", "{detailed: 9}", "{scans: -1}", "{detailed: -1}"} {
		if _, err := Parse([]byte("workers: " + in)); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
	on, off := Network{}, Network{AutoDetails: new(bool)}
	if !on.AutoDetailsOn() || off.AutoDetailsOn() {
		t.Error("auto_details")
	}
}
