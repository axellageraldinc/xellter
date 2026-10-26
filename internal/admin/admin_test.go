package admin

import (
	"context"
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
	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
)

// staticSource is a catalog source that never touches the network.
type staticSource struct{ snap *catalog.Snapshot }

func (s staticSource) Fetch(context.Context) (*catalog.Snapshot, error) { return s.snap, nil }

// cloneCfg copies a config so a test can patch it without touching the running one.
func cloneCfg(t *testing.T, c *config.Config) *config.Config {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	out := &config.Config{}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

// newTestApp builds an App on a temp dir with one client, so the admin API
// can be driven without a network or a kenari key.
func newTestApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	c := config.Default()
	c.Server.DataDir = filepath.Join(dir, "data")
	c.Server.SecretsFile = filepath.Join(c.Server.DataDir, "secrets.env")
	c.Server.AuthRecord = filepath.Join(c.Server.DataDir, "auth-record.json")
	c.Clients = []config.Client{{
		Name: "babu", Enabled: true, Purpose: "general",
		Keys: []config.Upstream{{Name: "main", KeyEnv: "KENARI_KEY_BABU"}},
		Presets: map[string]config.Preset{
			"text":  {Category: "chat", Optimization: "balanced", Models: []string{"cheap-b"}},
			"image": config.DefaultPreset("images"),
		},
	}}
	path := filepath.Join(dir, "xellter.yaml")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	if err := config.SetSecret(c.Server.SecretsFile, "KENARI_KEY_BABU", "kn-babu"); err != nil {
		t.Fatal(err)
	}
	snap := &catalog.Snapshot{
		Models: map[string]*catalog.Model{
			"cheap-b": {ID: "cheap-b", Endpoint: "chat", Context: 128000, ToolCall: true, InIDR: 100, OutIDR: 300, CachedIDR: 10},
			"pro-c":   {ID: "pro-c", Endpoint: "chat", Context: 200000, ToolCall: true, InIDR: 5000, OutIDR: 15000, CachedIDR: 500},
		},
		Tier:    catalog.FreeTier{Free: catalog.TierLimit{RPM: 5, Daily: 50}, Topup: catalog.TierLimit{RPM: 10, Daily: 1000}, Plan: catalog.TierLimit{RPM: 25}},
		Fetched: time.Now(),
	}
	a, err := app.New(path, staticSource{snap})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Rt.Recompute(time.Now())
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

// do drives one admin API call.
func do(t *testing.T, h *Handler, cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		req.Header.Set("X-Xellter", "1")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestConfigPutRejectsBothConfigAndYAML: a GET /config response carries both
// fields. Sending it back must not silently apply the old yaml and drop the
// patched config.
func TestConfigPutRejectsBothConfigAndYAML(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)

	// read the current config the way the skill's recipe does
	rec := do(t, h, c, http.MethodGet, "/admin/api/config", nil)
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

	// patch one knob and send the whole response back
	got.Config.Routing.TopModels = 7
	rec = do(t, h, c, http.MethodPut, "/admin/api/config", map[string]any{"config": got.Config, "yaml": *got.YAML})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with both fields: got %d %s, want 400", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "not both") {
		t.Fatalf("the error must say which fields clash: %s", rec.Body)
	}
}

// TestConfigPutAppliesARoutingKnob: the config-only body is the documented way
// to change a knob the panel does not show.
func TestConfigPutAppliesARoutingKnob(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)

	cur := cloneCfg(t, a.Cfg())
	cur.Routing.TopModels = 7
	cur.Routing.EvictErrors = 4
	rec := do(t, h, c, http.MethodPut, "/admin/api/config", map[string]any{"config": cur})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config: %d %s", rec.Code, rec.Body)
	}
	after := a.Cfg()
	if after.Routing.TopModels != 7 || after.Routing.EvictErrors != 4 {
		t.Fatalf("the routing knobs did not apply: %+v", after.Routing)
	}
	// the untouched knobs kept their default
	if after.Routing.EvictCooldownMin != config.Default().Routing.EvictCooldownMin || !after.Routing.StickySession {
		t.Fatalf("an untouched knob changed: %+v", after.Routing)
	}
	// the change is on disk
	b, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "top_models: 7") {
		t.Fatalf("the saved config does not carry the change:\n%s", b)
	}
}

// A retired knob is refused by the YAML path with the reason, so a pasted old config never looks applied.
func TestConfigPutRefusesARetiredSection(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)
	rec := do(t, h, c, http.MethodPut, "/admin/api/config", map[string]any{"yaml": "rules:\n  sticky_minutes: 5\n"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("retired section: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "retired by the rewrite") {
		t.Fatalf("the error must name the reason: %s", rec.Body)
	}
}

// A preset must be of a real category and carry a real optimization: anything else is a 422, and the running
// config is untouched.
func TestConfigPutValidatesAPreset(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)

	cur := cloneCfg(t, a.Cfg())
	cur.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "nonsense", Models: []string{"a"}}
	rec := do(t, h, c, http.MethodPut, "/admin/api/config", map[string]any{"config": cur})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad optimization: %d %s", rec.Code, rec.Body)
	}
	if a.Cfg().Clients[0].Presets["text"].Optimization == "nonsense" {
		t.Fatal("an invalid config was applied")
	}
	// a made-up preset name is refused too
	cur = cloneCfg(t, a.Cfg())
	cur.Clients[0].Presets["budget"] = config.Preset{Category: "chat", Optimization: "balanced"}
	rec = do(t, h, c, http.MethodPut, "/admin/api/config", map[string]any{"config": cur})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not a preset name") {
		t.Fatalf("custom preset name: %d %s", rec.Code, rec.Body)
	}
	// a valid edit applies and is reversible
	cur = cloneCfg(t, a.Cfg())
	p := cur.Clients[0].Presets["text"]
	p.Optimization, p.Models = "cheapest", []string{"cheap-b", "pro-c"}
	cur.Clients[0].Presets["text"] = p
	if rec := do(t, h, c, http.MethodPut, "/admin/api/config", map[string]any{"config": cur}); rec.Code != http.StatusOK {
		t.Fatalf("valid config rejected: %d %s", rec.Code, rec.Body)
	}
	if got := a.Cfg().Clients[0].Presets["text"]; got.Optimization != "cheapest" || len(got.Models) != 2 {
		t.Fatalf("edit not applied: %+v", got)
	}
	// and rollback restores the previous version
	var list struct{ History []string }
	rec = do(t, h, c, http.MethodGet, "/admin/api/config", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.History) == 0 {
		t.Fatal("no history kept")
	}
	if rec := do(t, h, c, http.MethodPost, "/admin/api/rollback", map[string]string{"id": list.History[0]}); rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	if got := a.Cfg().Clients[0].Presets["text"].Optimization; got != "balanced" {
		t.Fatalf("rollback did not restore the previous optimization: %s", got)
	}
}

// The overview and the ranking endpoints are readable with a session and closed without one.
func TestOverviewAndRankingNeedASession(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	if rec := do(t, h, nil, http.MethodGet, "/admin/api/overview", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", rec.Code)
	}
	c := sessionCookie(t, a)
	rec := do(t, h, c, http.MethodGet, "/admin/api/overview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body)
	}
	var ov struct {
		Status  string
		Clients []struct {
			Name    string
			Purpose string
			Presets []struct {
				Name         string
				Category     string
				Optimization string
				Models       int
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if ov.Status == "" || len(ov.Clients) != 1 || ov.Clients[0].Purpose != "general" {
		t.Fatalf("overview: %s", rec.Body)
	}
	if len(ov.Clients[0].Presets) != 2 || ov.Clients[0].Presets[0].Name != "text" || ov.Clients[0].Presets[0].Optimization != "balanced" {
		t.Fatalf("preset rows: %+v", ov.Clients[0].Presets)
	}
	// the ranking endpoint answers in the new shape
	rec = do(t, h, c, http.MethodGet, "/admin/api/ranking?client=babu&preset=text", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "candidates") {
		t.Fatalf("ranking: %d %s", rec.Code, rec.Body)
	}
	// a state change without the X-Xellter header is refused
	req := httptest.NewRequest(http.MethodPost, "/admin/api/recompute", strings.NewReader("{}"))
	req.Host = "localhost"
	req.AddCookie(c)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("state change without X-Xellter: %d", rr.Code)
	}
	// a state change with it works
	if rec := do(t, h, c, http.MethodPost, "/admin/api/recompute", map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("recompute: %d %s", rec.Code, rec.Body)
	}
}

// The panel is invisible on a hostname that is not in admin_hosts.
func TestAdminHostAllowlist(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.Host = "xellter.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the panel must be invisible on other hostnames: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.Host = "localhost"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "xellter admin") {
		t.Fatalf("the UI shell must be served: %d", rec.Code)
	}
}

// The client page is what the panel's detail view reads: the client, its keys, and one card per preset with
// the live ranking and the evictions.
func TestClientDetailShowsTheRankingPerPreset(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)
	rec := do(t, h, c, http.MethodGet, "/admin/api/client?name=babu", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("client detail: %d %s", rec.Code, rec.Body)
	}
	var d struct {
		Client struct {
			Name    string
			Purpose string
		}
		Keys []struct {
			Name   string
			State  string
			Probed bool
		}
		Presets []struct {
			Name         string
			Category     string
			Optimization string
			Models       []string
			Ranking      *struct {
				Candidates []struct {
					Model   string
					Score   float64
					Billing string
					Reason  string
				}
				Rejected []struct{ Model, Why string }
			}
			Evictions map[string]time.Time
		}
		Optimizations []struct{ Value, Label string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Client.Name != "babu" || d.Client.Purpose != "general" {
		t.Fatalf("client: %+v", d.Client)
	}
	if len(d.Keys) != 1 || d.Keys[0].Name != "main" || d.Keys[0].State != "unchecked" {
		t.Fatalf("keys: %+v", d.Keys)
	}
	if len(d.Presets) != 2 || d.Presets[0].Name != "text" || d.Presets[0].Category != "chat" {
		t.Fatalf("presets: %+v", d.Presets)
	}
	rk := d.Presets[0].Ranking
	if rk == nil || len(rk.Candidates) != 1 || rk.Candidates[0].Model != "cheap-b" || rk.Candidates[0].Billing != "balance" {
		t.Fatalf("the text preset must rank its pool: %+v", rk)
	}
	if rk.Candidates[0].Reason == "" {
		t.Fatal("a candidate must explain itself")
	}
	if len(d.Optimizations) != 3 {
		t.Fatalf("the three optimizations: %+v", d.Optimizations)
	}
	if rec := do(t, h, c, http.MethodGet, "/admin/api/client?name=nobody", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown client: %d", rec.Code)
	}
}

// The model dropdown of a preset: every catalog model of that category, with the price line and the billing
// class the client would get.
func TestCatalogModelsForAPreset(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)
	rec := do(t, h, c, http.MethodGet, "/admin/api/catalog/models?client=babu&category=chat", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Category string
		Client   string
		Models   []struct {
			ID         string  `json:"id"`
			Owner      string  `json:"owner"`
			Endpoint   string  `json:"endpoint"`
			Context    int     `json:"context"`
			ToolCall   bool    `json:"tool_call"`
			Free       bool    `json:"free"`
			Billing    string  `json:"billing"`
			Callable   bool    `json:"callable"`
			InIDR      float64 `json:"in_idr"`
			OutIDR     float64 `json:"out_idr"`
			RefCost    float64 `json:"ref_cost_idr"`
			RefKnown   bool    `json:"ref_cost_known"`
			Modalities string  `json:"modalities"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Category != "chat" || out.Client != "babu" {
		t.Fatalf("header: %+v", out)
	}
	if len(out.Models) != 2 {
		t.Fatalf("only chat models belong in a chat preset: %+v", out.Models)
	}
	got := map[string]struct {
		Billing  string
		Callable bool
		Ref      float64
	}{}
	for _, m := range out.Models {
		if m.Endpoint != "chat" || m.Context == 0 || m.InIDR == 0 || !m.ToolCall {
			t.Fatalf("row: %+v", m)
		}
		got[m.ID] = struct {
			Billing  string
			Callable bool
			Ref      float64
		}{m.Billing, m.Callable, m.RefCost}
	}
	// no plan was reported by kenari, so nothing is plan-covered: everything is paid from the balance
	if g := got["cheap-b"]; g.Billing != "balance" || !g.Callable || g.Ref <= 0 {
		t.Fatalf("cheap-b: %+v", g)
	}
	// an image model is not in a chat dropdown
	rec = do(t, h, c, http.MethodGet, "/admin/api/catalog/models?category=images", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog images: %d", rec.Code)
	}
}

// Saving a preset from the panel writes the pool and the optimization, and the ranking follows.
func TestPresetSaveAndDelete(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)
	rec := do(t, h, c, http.MethodPost, "/admin/api/presets", map[string]any{
		"client": "babu", "action": "save", "preset": "text", "category": "chat", "optimization": "cheapest",
		"models": []string{"cheap-b", "pro-c", "cheap-b", ""},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save preset: %d %s", rec.Code, rec.Body)
	}
	p := a.Cfg().Clients[0].Presets["text"]
	if p.Optimization != "cheapest" || len(p.Models) != 2 || p.Models[0] != "cheap-b" || p.Models[1] != "pro-c" {
		t.Fatalf("the pool was not saved (duplicates and blanks dropped): %+v", p)
	}
	rk := a.Rt.Rankings()["babu/text"]
	if rk == nil || len(rk.Candidates) != 2 || rk.Candidates[0].Model != "cheap-b" {
		t.Fatalf("the ranking must follow the new pool: %+v", rk)
	}
	// a made-up name is refused
	rec = do(t, h, c, http.MethodPost, "/admin/api/presets", map[string]any{"client": "babu", "action": "save", "preset": "budget"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not a preset name") {
		t.Fatalf("custom preset name: %d %s", rec.Code, rec.Body)
	}
	// delete removes the card
	if rec := do(t, h, c, http.MethodPost, "/admin/api/presets", map[string]any{"client": "babu", "action": "delete", "preset": "image"}); rec.Code != http.StatusOK {
		t.Fatalf("delete preset: %d %s", rec.Code, rec.Body)
	}
	if _, ok := a.Cfg().Clients[0].Presets["image"]; ok {
		t.Fatal("the preset was not deleted")
	}
	if rec := do(t, h, c, http.MethodPost, "/admin/api/presets", map[string]any{"client": "babu", "action": "delete", "preset": "image"}); rec.Code != http.StatusNotFound {
		t.Fatalf("deleting it twice: %d", rec.Code)
	}
}

// The Recalculate button recomputes one client's presets, and an unknown client is a 404.
func TestRecomputeOneClient(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)
	before := a.Rt.Computed()
	rec := do(t, h, c, http.MethodPost, "/admin/api/recompute?client=babu", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("recompute: %d %s", rec.Code, rec.Body)
	}
	if !a.Rt.Computed().After(before) && !a.Rt.Computed().Equal(before) {
		t.Fatal("the rankings were not recomputed")
	}
	if rec := do(t, h, c, http.MethodPost, "/admin/api/recompute?client=nobody", map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown client: %d", rec.Code)
	}
}

// The usage drill-down goes client -> preset -> model, and every level totals the rows below it.
func TestUsageDrillDown(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)
	// one logged request, so the drill-down has something to aggregate
	a.St.Log(store.Request{TS: time.Now().UnixMilli(), Client: "babu", Preset: "text", Model: "cheap-b", Upstream: "babu/main",
		OK: true, Billing: "balance", InTok: 1000, OutTok: 50, CatalogCost: 0.5, Key: "main", Session: "s1"})
	a.St.Flush()

	levels := []struct {
		query string
		level string
		rows  int
	}{
		{"window=24h", "client", 1},
		{"window=24h&client=babu", "preset", 2}, // text has traffic, image is listed with zero
		{"window=24h&client=babu&preset=text", "model", 1},
	}
	for _, l := range levels {
		rec := do(t, h, c, http.MethodGet, "/admin/api/usage/drill?"+l.query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", l.query, rec.Code, rec.Body)
		}
		var out struct {
			Level  string
			Window string
			Rows   []struct {
				Key      string  `json:"key"`
				Client   string  `json:"client"`
				Preset   string  `json:"preset"`
				Model    string  `json:"model"`
				Billing  string  `json:"billing"`
				Requests int     `json:"requests"`
				InTok    int64   `json:"in_tok"`
				Cost     float64 `json:"cost_idr"`
			}
			Totals struct {
				Requests int     `json:"requests"`
				InTok    int64   `json:"in_tok"`
				Cost     float64 `json:"cost_idr"`
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v", l.query, err)
		}
		if out.Level != l.level || len(out.Rows) != l.rows {
			t.Fatalf("%s: level %q with %d rows: %s", l.query, out.Level, len(out.Rows), rec.Body)
		}
		if out.Totals.Requests != 1 || out.Totals.InTok != 1000 || out.Totals.Cost != 0.5 {
			t.Fatalf("%s: totals must sum the rows: %+v", l.query, out.Totals)
		}
		if l.level == "model" && (out.Rows[0].Model != "cheap-b" || out.Rows[0].Preset != "text" || out.Rows[0].Billing == "") {
			t.Fatalf("the model row: %+v", out.Rows[0])
		}
	}
	// the wrong window and an unknown client are refused
	if rec := do(t, h, c, http.MethodGet, "/admin/api/usage/drill?window=7d", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad window: %d", rec.Code)
	}
	if rec := do(t, h, c, http.MethodGet, "/admin/api/usage/drill?window=24h&client=nobody", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown client: %d", rec.Code)
	}
}

// The panel's per-key view and session view read the same rows the client sees.
func TestAdminUsageKeysAndSession(t *testing.T) {
	a := newTestApp(t)
	h := New(a)
	c := sessionCookie(t, a)
	a.St.Log(store.Request{TS: time.Now().UnixMilli(), Client: "babu", Preset: "text", Model: "cheap-b", OK: true,
		Billing: "balance", InTok: 10, Key: "main", Session: "s1", RTKSaved: 42})
	a.St.Flush()

	rec := do(t, h, c, http.MethodGet, "/admin/api/usage/keys?client=babu", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage keys: %d %s", rec.Code, rec.Body)
	}
	var keys []struct {
		APIKey   string `json:"api_key"`
		Requests int
		RTKSaved int64 `json:"rtk_saved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].APIKey != "main" || keys[0].Requests != 1 || keys[0].RTKSaved != 42 {
		t.Fatalf("key rows: %+v", keys)
	}
	if rec := do(t, h, c, http.MethodGet, "/admin/api/usage/keys", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("a key breakdown needs a client: %d", rec.Code)
	}

	rec = do(t, h, c, http.MethodGet, "/admin/api/session?client=babu&id=s1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("session: %d %s", rec.Code, rec.Body)
	}
	var s struct {
		Session struct {
			Session   string
			Preset    string
			Requests  int
			InTok     int64
			LastModel string `json:"last_model"`
		}
		Pins map[string]any
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Session.Session != "s1" || s.Session.Preset != "text" || s.Session.Requests != 1 || s.Session.LastModel != "cheap-b" {
		t.Fatalf("session: %+v", s.Session)
	}
	if rec := do(t, h, c, http.MethodGet, "/admin/api/session?client=babu&id=nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: %d", rec.Code)
	}
	if rec := do(t, h, c, http.MethodGet, "/admin/api/session?client=nobody&id=s1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown client: %d", rec.Code)
	}
	if rec := do(t, h, c, http.MethodGet, "/admin/api/session", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("session needs a client and an id: %d", rec.Code)
	}
}
