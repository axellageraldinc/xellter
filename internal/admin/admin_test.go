package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/config"
)

// newTestApp builds an App on a temp dir with one client, so the admin API
// can be driven without a network or a kenari key.
func newTestApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	c := config.Default()
	c.Server.DataDir = filepath.Join(dir, "data")
	c.Server.SecretsFile = filepath.Join(c.Server.DataDir, "secrets.env")
	c.Server.AuthRecord = filepath.Join(c.Server.DataDir, "auth-record.json")
	c.Clients = []config.Client{{Name: "babu", Enabled: true, Keys: []config.Upstream{{Name: "main", KeyEnv: "KENARI_KEY_BABU"}}, Presets: config.DefaultPresets()}}
	path := filepath.Join(dir, "xellter.yaml")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.St.Close() })
	return a
}

// sessionCookie logs in and returns the session cookie for the admin API.
func sessionCookie(t *testing.T, a *app.App) *http.Cookie {
	t.Helper()
	rec, _, err := auth.NewRecord("admin", "secret-password-123")
	if err != nil {
		t.Fatal(err)
	}
	a.Auth = auth.NewAuthenticator(&rec, filepath.Join(a.Cfg().Server.DataDir, "login-state.json"), filepath.Join(a.Cfg().Server.DataDir, "recovery-used.json"))
	tok, hash := auth.NewToken()
	a.St.PutSession(hash, time.Now().Add(time.Hour), "127.0.0.1", "test")
	return &http.Cookie{Name: cookieName, Value: tok, Path: "/admin"}
}

// TestConfigPutRejectsBothConfigAndYAML: a GET /config response carries both
// fields. Sending it back must not silently apply the old yaml and drop the
// patched config.
func TestConfigPutRejectsBothConfigAndYAML(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)

	// read the current config the way the skill's recipe does
	req := httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)
	req.Host = "localhost"
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Config *config.Config `json:"config"`
		YAML   *string        `json:"yaml"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Config == nil || got.YAML == nil {
		t.Fatal("GET /config must return both the config and the yaml")
	}

	// patch one hidden knob and send the whole response back
	got.Config.Rules.FreeBonus = 0.25
	body, _ := json.Marshal(map[string]any{"config": got.Config, "yaml": *got.YAML})
	req = httptest.NewRequest(http.MethodPut, "/admin/api/config", strings.NewReader(string(body)))
	req.Host = "localhost"
	req.Header.Set("X-Xellter", "1")
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with both fields: got %d %s, want 400", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "not both") {
		t.Fatalf("the error must say which fields clash: %s", rec.Body)
	}
}

// TestConfigPutAppliesAHiddenKnob: the config-only body is the documented way
// to change a knob the panel does not show.
func TestConfigPutAppliesAHiddenKnob(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)

	cur := a.Cfg()
	cur.Rules.FreeBonus = 0.25
	cur.RTK.MinChars = 500
	body, _ := json.Marshal(map[string]any{"config": cur})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/config", strings.NewReader(string(body)))
	req.Host = "localhost"
	req.Header.Set("X-Xellter", "1")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config: %d %s", rec.Code, rec.Body)
	}
	after := a.Cfg()
	if after.Rules.FreeBonus != 0.25 || after.RTK.MinChars != 500 {
		t.Fatalf("the hidden knobs did not apply: %+v %+v", after.Rules, after.RTK)
	}
	// the untouched hidden knobs kept their default
	if after.Rules.PlanBonus != config.Default().Rules.PlanBonus || after.RTK.DiffContext != config.Default().RTK.DiffContext {
		t.Fatalf("an untouched hidden knob changed: %v %v", after.Rules.PlanBonus, after.RTK.DiffContext)
	}
	// the change is on disk
	b, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "free_bonus: 0.25") {
		t.Fatalf("the saved config does not carry the change:\n%s", b)
	}
}
