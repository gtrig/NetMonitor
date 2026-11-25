package config

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestHub(t *testing.T) {
	for hub, want := range map[string]string{"10.0.0.1": "", "": "", "10.0.1.1": "hub", "router": "hub", "10.0.0.9": "excluded"} {
		_, err := Parse([]byte(`
networks:
  - {name: lan, cidr: 10.0.0.0/24, hub: "` + hub + `", exclude: [10.0.0.9], sources: [{type: nmap}]}`))
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: %v", hub, err)
		}
	}
	// It survives saving, and is left out when empty.
	var n Network
	if err := json.Unmarshal([]byte(`{"cidr":"10.0.0.0/24","hub":"10.0.0.1"}`), &n); err != nil {
		t.Fatal(err)
	}
	if out, _ := yaml.Marshal(n); !strings.Contains(string(out), "hub: 10.0.0.1") {
		t.Errorf("%s", out)
	}
	n.Hub = ""
	if out, _ := yaml.Marshal(n); strings.Contains(string(out), "hub") {
		t.Errorf("%s", out)
	}
}
