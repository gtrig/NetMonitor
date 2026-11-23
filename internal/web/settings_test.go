package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtrig/netmon/internal/config"
	"github.com/gtrig/netmon/internal/settings"
	"github.com/gtrig/netmon/internal/store"
)

type fakeSettings struct {
	raw      *config.Config
	saved    *config.Config
	saveErr  error
	reloaded int
}

func (f *fakeSettings) Raw() *config.Config { c := *f.raw; return &c }
func (f *fakeSettings) Save(c *config.Config, _ map[string]string) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = c
	return nil
}
func (f *fakeSettings) Reload() error { f.reloaded++; return nil }
func (f *fakeSettings) Path() string  { return "/config/config.yaml" }

func newSettingsTest(t *testing.T, auth *config.BasicAuth) (http.Handler, *fakeSettings) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	raw := &config.Config{
		Routers:  []config.Router{{Name: "core", Type: "mikrotik", Address: "10.0.0.1:8728", Password: "hunter2"}},
		Networks: []config.Network{{Name: "lan", CIDR: netip.MustParsePrefix("10.0.0.0/24"), Sources: []config.SourceRef{{Type: "router", Ref: "core"}}}},
		Channels: []config.Channel{{Name: "tg", Type: "telegram", BotToken: "${TG}", ChatID: "1"}},
	}
	fs := &fakeSettings{raw: raw}
	cfg := &config.Config{Auth: auth}
	h := New(Deps{Store: st, Settings: fs, Runtime: func() Runtime {
		return Runtime{Scheduler: &fakeSched{}, Notifier: fakeNotifier{}, Config: cfg}
	}})
	return h, fs
}

func TestGetSettingsMasksSecrets(t *testing.T) {
	h, _ := newSettingsTest(t, creds)
	rr := do(h, "GET", "/api/settings", "", true)
	if rr.Code != 200 {
		t.Fatalf("%d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "hunter2") || !strings.Contains(body, "${TG}") || !strings.Contains(body, `"can_edit":true`) {
		t.Fatalf("body = %s", body)
	}
}

func TestSettingsReadOnlyWithoutAuth(t *testing.T) {
	h, fs := newSettingsTest(t, nil)
	rr := do(h, "GET", "/api/settings", "", false)
	if !strings.Contains(rr.Body.String(), `"can_edit":false`) {
		t.Fatalf("body = %s", rr.Body)
	}
	if rr := do(h, "PUT", "/api/settings", `{"networks":[]}`, false); rr.Code != 403 || fs.saved != nil {
		t.Fatalf("PUT without auth: %d", rr.Code)
	}
}

func TestPutSettingsAppliesAndKeepsSecret(t *testing.T) {
	h, fs := newSettingsTest(t, creds)
	e := settings.Mask(fs.raw)
	e.Networks[0].Name = "home"
	b, _ := json.Marshal(e)
	rr := do(h, "PUT", "/api/settings", string(b), true)
	if rr.Code != 204 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if fs.saved.Networks[0].Name != "home" || fs.saved.Routers[0].Password != "hunter2" {
		t.Fatalf("saved = %+v", fs.saved)
	}
}

func TestPutSettingsValidationErrors(t *testing.T) {
	h, fs := newSettingsTest(t, creds)
	fs.saveErr = errors.Join(errors.New(`network "lan": unknown source type "x"`), errors.New(`rule 1: unknown channel "y"`))
	rr := do(h, "PUT", "/api/settings", `{"networks":[]}`, true)
	var out struct{ Errors []string }
	json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != 400 || len(out.Errors) != 2 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
}

func TestReloadSettings(t *testing.T) {
	h, fs := newSettingsTest(t, creds)
	if rr := do(h, "POST", "/api/settings/reload", "", true); rr.Code != 204 || fs.reloaded != 1 {
		t.Fatalf("%d", rr.Code)
	}
}

func TestSettingsPageRenders(t *testing.T) {
	h, _ := newSettingsTest(t, nil)
	req := httptest.NewRequest("GET", "/settings", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req.WithContext(context.Background()))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "<h1>Settings</h1>") {
		t.Fatalf("%d", rr.Code)
	}
	for _, tab := range []string{"networks", "sources", "notifications", "system"} {
		if !strings.Contains(rr.Body.String(), `aria-controls="panel-`+tab+`"`) || !strings.Contains(rr.Body.String(), `id="panel-`+tab+`"`) {
			t.Errorf("missing tab or panel %q", tab)
		}
	}
}
