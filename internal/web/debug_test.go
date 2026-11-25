package web

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/debug"
	"github.com/gtrig/netmon/internal/settings"
)

type fakeDebug struct{ err error }

func (f *fakeDebug) Status() debug.Status      { return debug.Status{Running: true, Addr: "127.0.0.1:8099"} }
func (f *fakeDebug) NewToken() (string, error) { return "fresh", f.err }

func debugHandler(auth *config.BasicAuth, d Debug) http.Handler {
	return New(Deps{Debug: d, Settings: &fakeSettings{raw: &config.Config{}}, Runtime: func() Runtime {
		return Runtime{Scheduler: &fakeSched{}, Config: &config.Config{Auth: auth}}
	}})
}

func TestDebugAPI(t *testing.T) {
	f := &fakeDebug{}
	h := debugHandler(creds, f)
	if rr := do(h, "GET", "/api/debug", "", true); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"addr":"127.0.0.1:8099"`) || strings.Contains(rr.Body.String(), "fresh") {
		t.Error(rr.Code, rr.Body)
	}
	if rr := do(h, "POST", "/api/debug/token", "", true); rr.Code != 200 || !strings.Contains(rr.Body.String(), "fresh") || rr.Header().Get("Cache-Control") != "no-store" {
		t.Error(rr.Code, rr.Body)
	}
	if rr := do(h, "POST", "/api/debug/token", "", false); rr.Code != 401 {
		t.Error("without login:", rr.Code)
	}
	f.err = debug.ErrTokenGiven
	if rr := do(h, "POST", "/api/debug/token", "", true); rr.Code != 409 {
		t.Error("token from the environment:", rr.Code)
	}
	if rr := do(debugHandler(nil, &fakeDebug{}), "POST", "/api/debug/token", "", false); rr.Code != 403 {
		t.Error("without auth configured:", rr.Code)
	}
	if rr := do(debugHandler(creds, nil), "GET", "/api/debug", "", true); rr.Code != 404 {
		t.Error("without debug mode:", rr.Code)
	}
}

func TestDebugSettings(t *testing.T) {
	h, fs := newSettingsTest(t, creds)
	fs.raw.Debug = config.Debug{Enabled: true, Allow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}
	if rr := do(h, "GET", "/api/settings", "", true); !strings.Contains(rr.Body.String(), `"debug":{"enabled":true,"listen":"","allow":["10.0.0.0/24"]}`) {
		t.Error(rr.Body)
	}
	// Not sent: kept.
	e := settings.Mask(fs.raw)
	e.Debug = nil
	b, _ := json.Marshal(e)
	if rr := do(h, "PUT", "/api/settings", string(b), true); rr.Code != 204 || !fs.saved.Debug.Enabled || len(fs.saved.Debug.Allow) != 1 {
		t.Error(rr.Code, fs.saved.Debug)
	}
	e.Debug = &config.Debug{Listen: "127.0.0.1:1"}
	b, _ = json.Marshal(e)
	if rr := do(h, "PUT", "/api/settings", string(b), true); rr.Code != 204 || fs.saved.Debug.Enabled || fs.saved.Debug.Listen != "127.0.0.1:1" {
		t.Error(rr.Code, fs.saved.Debug)
	}
	if rr := do(h, "PUT", "/api/settings", `{"debug":{"allow":["lan"]}}`, true); rr.Code != 400 {
		t.Error(rr.Code)
	}
}
