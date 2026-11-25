package main

import "testing"

func TestParseTargets(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"192.168.8.12"}, 1},
		{[]string{"192.168.8.1/24"}, 254},
		{[]string{"192.168.8.1/255.255.255.0"}, 254},
		{[]string{"192.168.8.1/24", "192.168.88.1/24"}, 508},
		{[]string{"192.168.8.1/24", "192.168.8.0/25", "192.168.8.12"}, 254},
	} {
		if got, err := parseTargets(c.args); err != nil || len(got) != c.want {
			t.Errorf("%v: %d addresses, %v; want %d", c.args, len(got), err, c.want)
		}
	}
	for _, bad := range []string{"", "x", "10.0.0.1/", "10.0.0.1/33", "10.0.0.1/-1", "10.0.0.1/abc", "10.0.0.1/255.0.255.0", "10.0.0.1/24/8", "10.0.0.0/8", "fd00::1", "fd00::/120"} {
		if got, err := parseTargets([]string{"10.0.0.1", bad}); err == nil {
			t.Errorf("%q accepted: %d addresses", bad, len(got))
		}
	}
}
