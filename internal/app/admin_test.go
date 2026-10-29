package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/admin"
	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/config"
)

type adminEnv struct {
	*env
	srv    *httptest.Server
	secret string
	pw     string
}

func setupAdmin(t *testing.T) *adminEnv {
	t.Helper()
	ae := &adminEnv{pw: "correct horse battery staple"}
	rec, _, err := auth.NewRecord("axell", ae.pw)
	if err != nil {
		t.Fatal(err)
	}
	ae.secret = rec.TotpSecret
	ae.env = setup(t, func(c *config.Config) {
		c.Server.AdminHosts = []string{"127.0.0.1", "localhost"}
		_ = os.MkdirAll(c.Server.DataDir, 0o700)
		if err := auth.WriteRecord(filepath.Join(c.Server.DataDir, "auth.json"), rec); err != nil {
			t.Fatal(err)
		}
	})
	mux := http.NewServeMux()
	panel := admin.New(ae.a)
	mux.Handle("/api/", panel)
	mux.Handle("/v1/", ae.a.Px)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { ae.a.Px.ServeHTTP(w, r) })
	mux.Handle("/", panel)
	ae.srv = httptest.NewServer(mux)
	t.Cleanup(ae.srv.Close)
	return ae
}

func (ae *adminEnv) do(t *testing.T, cookie *http.Cookie, method, path string, body any, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ae.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if method != "GET" {
		req.Header.Set("X-Xellter", "1")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (ae *adminEnv) login(t *testing.T) *http.Cookie {
	t.Helper()
	resp, b := ae.do(t, nil, "POST", "/api/login", map[string]string{"username": "axell", "password": ae.pw, "code": auth.TOTPNow(ae.secret, time.Now())})
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d %s", resp.StatusCode, b)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "xl_session" {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatal("session cookie must be HttpOnly + SameSite=Strict")
			}
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestAdminLoginRequiresPasswordAndSecondFactor(t *testing.T) {
	ae := setupAdmin(t)
	if r, _ := ae.do(t, nil, "GET", "/api/overview", nil); r.StatusCode != 401 {
		t.Fatalf("API open without login: %d", r.StatusCode)
	}
	bad := []map[string]string{
		{"username": "axell", "password": ae.pw, "code": "000000"},
		{"username": "axell", "password": "wrong password here", "code": auth.TOTPNow(ae.secret, time.Now())},
		{"username": "other", "password": ae.pw, "code": auth.TOTPNow(ae.secret, time.Now())},
	}
	for i, b := range bad {
		if r, _ := ae.do(t, nil, "POST", "/api/login", b); r.StatusCode != 401 {
			t.Fatalf("case %d must be rejected, got %d", i, r.StatusCode)
		}
	}
	// the three failures above used up part of the 5-attempt budget; a valid login still works
	ck := ae.login(t)
	if r, b := ae.do(t, ck, "GET", "/api/overview", nil); r.StatusCode != 200 || !strings.Contains(string(b), `"keys"`) {
		t.Fatalf("overview: %d %s", r.StatusCode, b)
	}
}

func TestAdminBruteForceIsRateLimited(t *testing.T) {
	ae := setupAdmin(t)
	var last int
	for i := 0; i < 7; i++ {
		r, _ := ae.do(t, nil, "POST", "/api/login", map[string]string{"username": "axell", "password": "nope nope nope nope", "code": "123456"})
		last = r.StatusCode
	}
	if last != 429 {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
	r, _ := ae.do(t, nil, "POST", "/api/login", map[string]string{"username": "axell", "password": ae.pw, "code": auth.TOTPNow(ae.secret, time.Now())})
	if r.StatusCode != 429 {
		t.Fatalf("even the right credentials must wait out the lockout, got %d", r.StatusCode)
	}
}

func TestAdminHostAllowlistAndCSRF(t *testing.T) {
	ae := setupAdmin(t)
	ck := ae.login(t)
	req, _ := http.NewRequest("GET", ae.srv.URL+"/", nil)
	req.Host = "xellter.example.com"
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 404 {
		t.Fatalf("panel must be invisible on other hostnames: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("PUT", ae.srv.URL+"/api/config", strings.NewReader(`{}`))
	req.AddCookie(ck)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("state change without X-Xellter header must be refused: %d", resp.StatusCode)
	}
}

func TestAdminConfigClientsAndRollback(t *testing.T) {
	ae := setupAdmin(t)
	ck := ae.login(t)

	// create a client and give it a kenari key: presets appear on their own, the xellter key works at once
	resp, b := ae.do(t, ck, "POST", "/api/clients", map[string]any{"action": "create", "name": "polifin"})
	var created struct{ Key string }
	_ = json.Unmarshal(b, &created)
	if resp.StatusCode != 200 || !strings.HasPrefix(created.Key, "xl-") {
		t.Fatalf("create client: %d %s", resp.StatusCode, b)
	}
	if r, b := ae.do(t, ck, "POST", "/api/keys", map[string]any{"client": "polifin", "action": "add", "name": "main", "key": "kn-polifin"}); r.StatusCode != 200 {
		t.Fatalf("add key: %d %s", r.StatusCode, b)
	}
	ae.a.ProbeKeys(context.Background())
	var polifin config.Client
	for _, c := range ae.a.Cfg().Clients {
		if c.Name == "polifin" {
			polifin = c
		}
	}
	// presets are not generated any more: a new client starts empty and the owner picks its models
	if len(polifin.Presets) != 0 {
		t.Fatalf("a new client must start with no presets: %+v", polifin.Presets)
	}
	// the key authenticates, but with no preset configured there is nothing to route to
	if r, _ := ae.post(t, created.Key, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}]}`); r.StatusCode != 404 {
		t.Fatalf("a client without presets must not serve: %d", r.StatusCode)
	}
	// give it a preset and it serves at once
	_, b = ae.do(t, ck, "GET", "/api/config", nil)
	var full struct{ Config map[string]any }
	_ = json.Unmarshal(b, &full)
	for _, c := range full.Config["clients"].([]any) {
		cl := c.(map[string]any)
		if cl["name"] == "polifin" {
			cl["presets"] = map[string]any{"text": map[string]any{"category": "chat", "optimization": "cheapest", "models": []any{"cheap-b"}}}
		}
	}
	if r, b := ae.do(t, ck, "PUT", "/api/config", map[string]any{"config": full.Config}); r.StatusCode != 200 {
		t.Fatalf("config with a preset: %d %s", r.StatusCode, b)
	}
	if r, b := ae.post(t, created.Key, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}]}`); r.StatusCode != 200 {
		t.Fatalf("new client key rejected: %d %s", r.StatusCode, b)
	}
	if r, _ := ae.post(t, created.Key, "/v1/chat/completions", `{"model":"pro-c","messages":[{"role":"user","content":"hi"}]}`); r.StatusCode != 403 {
		t.Fatalf("a model outside the pool must be refused: %d", r.StatusCode)
	}
	data, _ := os.ReadFile(ae.a.Path)
	if strings.Contains(string(data), created.Key) || strings.Contains(string(data), "kn-polifin") {
		t.Fatal("plain client key or kenari key must never be written to the config")
	}
	if ae.a.Secrets()["KENARI_KEY_POLIFIN_MAIN"] != "kn-polifin" {
		t.Fatal("kenari key not stored in the secrets file")
	}

	// rotate: old key dies
	resp, b = ae.do(t, ck, "POST", "/api/clients", map[string]any{"action": "rotate", "name": "polifin"})
	var rot struct{ Key string }
	_ = json.Unmarshal(b, &rot)
	if r, _ := ae.post(t, created.Key, "/v1/chat/completions", `{"model":"text","messages":[]}`); r.StatusCode != 401 {
		t.Fatalf("old key still valid: %d", r.StatusCode)
	}
	if rot.Key == "" || rot.Key == created.Key {
		t.Fatal("rotation must issue a new key")
	}

	// an invalid config is rejected and nothing changes
	_, b = ae.do(t, ck, "GET", "/api/config", nil)
	var cur struct {
		Config  map[string]any
		History []string
	}
	_ = json.Unmarshal(b, &cur)
	text := func() map[string]any {
		return cur.Config["clients"].([]any)[0].(map[string]any)["presets"].(map[string]any)["text"].(map[string]any)
	}
	text()["optimization"] = "nonsense"
	if r, _ := ae.do(t, ck, "PUT", "/api/config", map[string]any{"config": cur.Config}); r.StatusCode != 422 {
		t.Fatalf("invalid config accepted: %d", r.StatusCode)
	}
	if ae.a.Cfg().Clients[0].Presets["text"].Optimization == "nonsense" {
		t.Fatal("invalid config was applied")
	}

	// a preset name of the owner's own is accepted, as long as it states its category
	presetsOf := func(i int) map[string]any {
		return cur.Config["clients"].([]any)[i].(map[string]any)["presets"].(map[string]any)
	}
	text()["optimization"] = "balanced"
	presetsOf(0)["my-preset"] = map[string]any{"category": "chat", "optimization": "cheapest"}
	delete(presetsOf(0), "text")
	if r, b := ae.do(t, ck, "PUT", "/api/config", map[string]any{"config": cur.Config}); r.StatusCode != 200 {
		t.Fatalf("a preset of the owner's own name was refused: %d %s", r.StatusCode, b)
	}
	if _, ok := ae.a.Cfg().Clients[0].Presets["my-preset"]; !ok {
		t.Fatal("the renamed preset was not applied")
	}
	// a second chat preset is accepted (ADR 0038)
	presetsOf(0)["text"] = map[string]any{"category": "chat", "optimization": "balanced"}
	if r, b := ae.do(t, ck, "PUT", "/api/config", map[string]any{"config": cur.Config}); r.StatusCode != 200 {
		t.Fatalf("a second chat preset was refused: %d %s", r.StatusCode, b)
	}
	// a second preset of any other category is still refused
	presetsOf(0)["pic"] = map[string]any{"category": "images", "optimization": "balanced"}
	presetsOf(0)["pic2"] = map[string]any{"category": "images", "optimization": "balanced"}
	if r, b := ae.do(t, ck, "PUT", "/api/config", map[string]any{"config": cur.Config}); r.StatusCode != 422 {
		t.Fatalf("two presets of one category accepted: %d %s", r.StatusCode, b)
	}
	delete(presetsOf(0), "pic")
	delete(presetsOf(0), "pic2")
	delete(presetsOf(0), "text")
	presetsOf(0)["text"] = map[string]any{"category": "chat", "optimization": "balanced"}
	delete(presetsOf(0), "my-preset")
	if r, b := ae.do(t, ck, "PUT", "/api/config", map[string]any{"config": cur.Config}); r.StatusCode != 200 {
		t.Fatalf("the config was not restored: %d %s", r.StatusCode, b)
	}

	// a valid edit applies live and is reversible
	text()["optimization"] = "best_quality"
	if r, b := ae.do(t, ck, "PUT", "/api/config", map[string]any{"config": cur.Config}); r.StatusCode != 200 {
		t.Fatalf("valid config rejected: %d %s", r.StatusCode, b)
	}
	if ae.a.Cfg().Clients[0].Presets["text"].Optimization != "best_quality" {
		t.Fatal("edit not applied")
	}
	_, b = ae.do(t, ck, "GET", "/api/config", nil)
	_ = json.Unmarshal(b, &cur)
	if len(cur.History) == 0 {
		t.Fatal("no history kept")
	}
	if r, b := ae.do(t, ck, "POST", "/api/rollback", map[string]string{"id": cur.History[0]}); r.StatusCode != 200 {
		t.Fatalf("rollback: %d %s", r.StatusCode, b)
	}
	if got := ae.a.Cfg().Clients[0].Presets["text"].Optimization; got == "best_quality" {
		t.Fatalf("rollback did not restore the previous optimization: %s", got)
	}

	// ranking and the UI shell are served
	if r, b := ae.do(t, ck, "GET", "/api/ranking?client=babu&preset=text", nil); r.StatusCode != 200 || !strings.Contains(string(b), "candidates") {
		t.Fatalf("ranking: %d %s", r.StatusCode, b)
	}
	if r, b := ae.do(t, nil, "GET", "/", nil); r.StatusCode != 200 || !strings.Contains(string(b), "xellter") {
		t.Fatalf("ui: %d", r.StatusCode)
	}
}

func TestAdminKeysAndOverview(t *testing.T) {
	ae := setupAdmin(t)
	ck := ae.login(t)
	if r, b := ae.do(t, ck, "POST", "/api/keys", map[string]string{"client": "babu", "action": "add", "name": "spare key", "key": "kn-new-secret"}); r.StatusCode != 200 {
		t.Fatalf("add: %d %s", r.StatusCode, b)
	}
	found := false
	for _, k := range ae.a.Cfg().Clients[0].Keys {
		if k.Name == "spare-key" {
			found = true
			if k.KeyEnv != "KENARI_KEY_BABU_SPARE_KEY" {
				t.Fatalf("defaults wrong: %+v", k)
			}
		}
	}
	if !found || ae.a.Secrets()["KENARI_KEY_BABU_SPARE_KEY"] != "kn-new-secret" {
		t.Fatal("key not stored")
	}
	if cfg, _ := os.ReadFile(ae.a.Path); strings.Contains(string(cfg), "kn-new-secret") {
		t.Fatal("key value leaked into the config file")
	}
	if r, _ := ae.do(t, ck, "POST", "/api/keys", map[string]string{"client": "babu", "action": "add", "name": "spare-key", "key": "kn-x"}); r.StatusCode != 422 {
		t.Fatalf("duplicate key name accepted: %d", r.StatusCode)
	}
	_, b := ae.do(t, ck, "GET", "/api/overview", nil)
	var ov struct {
		Alerts  []struct{ Level, Text string }
		Status  string
		Clients []struct {
			Name    string
			Keys    []struct{ Label string }
			Presets []struct {
				Name string
				Pool int
			}
		}
	}
	if err := json.Unmarshal(b, &ov); err != nil || ov.Status == "" || len(ov.Clients) != 5 {
		t.Fatalf("overview incomplete: %v %s", err, b)
	}
	if len(ov.Clients[0].Keys) != 3 || len(ov.Clients[0].Presets) == 0 || ov.Clients[0].Presets[0].Name != "text" || ov.Clients[0].Presets[0].Pool == 0 {
		t.Fatalf("client row wrong: %+v", ov.Clients[0])
	}
	noKey := false
	for _, a := range ov.Alerts {
		if a.Level == "info" && strings.Contains(a.Text, "off has no API key") {
			noKey = true
		}
	}
	if !noKey {
		t.Fatalf("expected a hint for the client without keys: %+v", ov.Alerts)
	}
	if r, b := ae.do(t, ck, "POST", "/api/keys", map[string]string{"client": "babu", "action": "remove", "name": "spare-key"}); r.StatusCode != 200 {
		t.Fatalf("remove: %d %s", r.StatusCode, b)
	}
	if _, ok := ae.a.Secrets()["KENARI_KEY_BABU_SPARE_KEY"]; ok {
		t.Fatal("removed key must also leave the secrets file")
	}
	if r, _ := ae.do(t, ck, "POST", "/api/clients", map[string]string{"action": "delete", "name": "locked"}); r.StatusCode != 200 {
		t.Fatalf("client delete: %d", r.StatusCode)
	}
	if _, ok := ae.a.Secrets()["KEY_LOCKED"]; ok {
		t.Fatal("deleting a client must remove its kenari keys from the secrets file")
	}
	if r, b := ae.do(t, ck, "POST", "/api/account-key", map[string]string{"key": "kn-account"}); r.StatusCode != 200 || ae.a.Secrets()["KENARI_ACCOUNT_KEY"] != "kn-account" {
		t.Fatalf("account key: %d %s", r.StatusCode, b)
	}
}

func (ae *adminEnv) post(t *testing.T, key, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ae.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}
