package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rawYAML = `
listen: 0.0.0.0:8080
routers:
  - {name: core, type: mikrotik, address: 192.168.88.1:8729, tls: true, username: ro, password: "${CORE_PASS}"}
networks:
  - name: lan
    cidr: 192.168.88.0/24
    sources:
      - {type: router, ref: core, interval: 1m}
      - {type: nmap}
`

func writeTemp(t *testing.T, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRawKeepsReferencesAndSkipsDefaults(t *testing.T) {
	t.Setenv("CORE_PASS", "secret")
	raw, err := LoadRaw(writeTemp(t, rawYAML))
	if err != nil {
		t.Fatal(err)
	}
	if raw.Routers[0].Password != "${CORE_PASS}" {
		t.Fatalf("password = %q", raw.Routers[0].Password)
	}
	if raw.Networks[0].Sources[1].Interval.Duration != 0 || raw.DataDir != "" {
		t.Fatalf("defaults applied to raw: %+v", raw.Networks[0].Sources[1])
	}
}

func TestEffectiveExpandsAndValidates(t *testing.T) {
	t.Setenv("CORE_PASS", "secret")
	raw, _ := LoadRaw(writeTemp(t, rawYAML))
	eff, err := Effective(raw)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Routers[0].Password != "secret" || eff.Networks[0].Sources[1].Interval.Duration == 0 {
		t.Fatalf("effective = %+v", eff.Routers[0])
	}
	if raw.Routers[0].Password != "${CORE_PASS}" {
		t.Fatal("Effective mutated raw")
	}
	raw.Networks[0].Sources[0].Ref = "missing"
	if _, err := Effective(raw); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v", err)
	}
}

func TestSaveRoundTripIsMinimal(t *testing.T) {
	p := writeTemp(t, rawYAML)
	raw, _ := LoadRaw(p)
	raw.Networks = append(raw.Networks, Network{Name: "iot", CIDR: raw.Networks[0].CIDR,
		Sources: []SourceRef{{Type: "nmap"}}})
	raw.Networks[1].CIDR = mustPrefix(t, "192.168.20.0/24")
	if err := Save(p, raw); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	for _, want := range []string{"${CORE_PASS}", "192.168.20.0/24", "interval: 1m0s"} {
		if !strings.Contains(s, want) {
			t.Errorf("saved file lacks %q:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"data_dir", "auth:", "port: 0", "interval: 0s", "snmp_targets"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("saved file contains %q:\n%s", unwanted, s)
		}
	}
	again, err := LoadRaw(p)
	if err != nil || len(again.Networks) != 2 || again.Routers[0].Password != "${CORE_PASS}" {
		t.Fatalf("reload: %+v %v", again, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}
