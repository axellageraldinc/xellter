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
	mux.Handle("/admin/", admin.New(ae.a))
	mux.Handle("/", ae.a.Px)
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
	resp, b := ae.do(t, nil, "POST", "/admin/api/login", map[string]string{"username": "axell", "password": ae.pw, "code": auth.TOTPNow(ae.secret, time.Now())})
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
	if r, _ := ae.do(t, nil, "GET", "/admin/api/overview", nil); r.StatusCode != 401 {
		t.Fatalf("API open without login: %d", r.StatusCode)
	}
	bad := []map[string]string{
		{"username": "axell", "password": ae.pw, "code": "000000"},
		{"username": "axell", "password": "wrong password here", "code": auth.TOTPNow(ae.secret, time.Now())},
		{"username": "other", "password": ae.pw, "code": auth.TOTPNow(ae.secret, time.Now())},
	}
	for i, b := range bad {
		if r, _ := ae.do(t, nil, "POST", "/admin/api/login", b); r.StatusCode != 401 {
			t.Fatalf("case %d must be rejected, got %d", i, r.StatusCode)
		}
	}
	// the three failures above used up part of the 5-attempt budget; a valid login still works
	ck := ae.login(t)
	if r, b := ae.do(t, ck, "GET", "/admin/api/overview", nil); r.StatusCode != 200 || !strings.Contains(string(b), `"keys"`) {
		t.Fatalf("overview: %d %s", r.StatusCode, b)
	}
}

func TestAdminBruteForceIsRateLimited(t *testing.T) {
	ae := setupAdmin(t)
	var last int
	for i := 0; i < 7; i++ {
		r, _ := ae.do(t, nil, "POST", "/admin/api/login", map[string]string{"username": "axell", "password": "nope nope nope nope", "code": "123456"})
		last = r.StatusCode
	}
	if last != 429 {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
	r, _ := ae.do(t, nil, "POST", "/admin/api/login", map[string]string{"username": "axell", "password": ae.pw, "code": auth.TOTPNow(ae.secret, time.Now())})
	if r.StatusCode != 429 {
		t.Fatalf("even the right credentials must wait out the lockout, got %d", r.StatusCode)
	}
}

func TestAdminHostAllowlistAndCSRF(t *testing.T) {
	ae := setupAdmin(t)
	ck := ae.login(t)
	req, _ := http.NewRequest("GET", ae.srv.URL+"/admin/", nil)
	req.Host = "xellter.example.com"
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 404 {
		t.Fatalf("panel must be invisible on other hostnames: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("PUT", ae.srv.URL+"/admin/api/config", strings.NewReader(`{}`))
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
	resp, b := ae.do(t, ck, "POST", "/admin/api/clients", map[string]any{"action": "create", "name": "polifin"})
	var created struct{ Key string }
	_ = json.Unmarshal(b, &created)
	if resp.StatusCode != 200 || !strings.HasPrefix(created.Key, "xl-") {
		t.Fatalf("create client: %d %s", resp.StatusCode, b)
	}
	if r, b := ae.do(t, ck, "POST", "/admin/api/keys", map[string]any{"client": "polifin", "action": "add", "name": "main", "key": "kn-polifin"}); r.StatusCode != 200 {
		t.Fatalf("add key: %d %s", r.StatusCode, b)
	}
	ae.a.ProbeKeys(context.Background())
	var polifin config.Client
	for _, c := range ae.a.Cfg().Clients {
		if c.Name == "polifin" {
			polifin = c
		}
	}
	if _, ok := polifin.Presets["text"]; !ok || polifin.Presets["image"].Modality != "images" {
		t.Fatalf("presets were not generated: %+v", polifin.Presets)
	}
	if r, _ := ae.post(t, created.Key, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}]}`); r.StatusCode != 200 {
		t.Fatalf("new client key rejected: %d", r.StatusCode)
	}
	if r, _ := ae.post(t, created.Key, "/v1/chat/completions", `{"model":"cheap-b","messages":[{"role":"user","content":"hi"}]}`); r.StatusCode != 403 {
		t.Fatalf("direct models must be off by default: %d", r.StatusCode)
	}
	data, _ := os.ReadFile(ae.a.Path)
	if strings.Contains(string(data), created.Key) || strings.Contains(string(data), "kn-polifin") {
		t.Fatal("plain client key or kenari key must never be written to the config")
	}
	if ae.a.Secrets()["KENARI_KEY_POLIFIN_MAIN"] != "kn-polifin" {
		t.Fatal("kenari key not stored in the secrets file")
	}

	// rotate: old key dies
	resp, b = ae.do(t, ck, "POST", "/admin/api/clients", map[string]any{"action": "rotate", "name": "polifin"})
	var rot struct{ Key string }
	_ = json.Unmarshal(b, &rot)
	if r, _ := ae.post(t, created.Key, "/v1/chat/completions", `{"model":"text","messages":[]}`); r.StatusCode != 401 {
		t.Fatalf("old key still valid: %d", r.StatusCode)
	}
	if rot.Key == "" || rot.Key == created.Key {
		t.Fatal("rotation must issue a new key")
	}

	// an invalid config is rejected and nothing changes
	_, b = ae.do(t, ck, "GET", "/admin/api/config", nil)
	var cur struct {
		Config  map[string]any
		History []string
	}
	_ = json.Unmarshal(b, &cur)
	text := func() map[string]any {
		return cur.Config["clients"].([]any)[0].(map[string]any)["presets"].(map[string]any)["text"].(map[string]any)
	}
	text()["strategy"] = "nonsense"
	if r, _ := ae.do(t, ck, "PUT", "/admin/api/config", map[string]any{"config": cur.Config}); r.StatusCode != 422 {
		t.Fatalf("invalid config accepted: %d", r.StatusCode)
	}
	if ae.a.Cfg().Clients[0].Presets["text"].Strategy == "nonsense" {
		t.Fatal("invalid config was applied")
	}

	// a valid edit applies live and is reversible
	text()["strategy"] = "priority"
	if r, b := ae.do(t, ck, "PUT", "/admin/api/config", map[string]any{"config": cur.Config}); r.StatusCode != 200 {
		t.Fatalf("valid config rejected: %d %s", r.StatusCode, b)
	}
	if ae.a.Cfg().Clients[0].Presets["text"].Strategy != "priority" {
		t.Fatal("edit not applied")
	}
	_, b = ae.do(t, ck, "GET", "/admin/api/config", nil)
	_ = json.Unmarshal(b, &cur)
	if len(cur.History) == 0 {
		t.Fatal("no history kept")
	}
	if r, b := ae.do(t, ck, "POST", "/admin/api/rollback", map[string]string{"id": cur.History[0]}); r.StatusCode != 200 {
		t.Fatalf("rollback: %d %s", r.StatusCode, b)
	}
	if got := ae.a.Cfg().Clients[0].Presets["text"].Strategy; got != "round_robin" {
		t.Fatalf("rollback did not restore the previous strategy: %s", got)
	}

	// ranking and the UI shell are served
	if r, b := ae.do(t, ck, "GET", "/admin/api/ranking?client=babu&preset=text", nil); r.StatusCode != 200 || !strings.Contains(string(b), "candidates") {
		t.Fatalf("ranking: %d %s", r.StatusCode, b)
	}
	if r, b := ae.do(t, nil, "GET", "/admin/", nil); r.StatusCode != 200 || !strings.Contains(string(b), "xellter admin") {
		t.Fatalf("ui: %d", r.StatusCode)
	}
}

func TestAdminKeysAndOverview(t *testing.T) {
	ae := setupAdmin(t)
	ck := ae.login(t)
	if r, b := ae.do(t, ck, "POST", "/admin/api/keys", map[string]string{"client": "babu", "action": "add", "name": "spare key", "key": "kn-new-secret"}); r.StatusCode != 200 {
		t.Fatalf("add: %d %s", r.StatusCode, b)
	}
	found := false
	for _, k := range ae.a.Cfg().Clients[0].Keys {
		if k.Name == "spare-key" {
			found = true
			if k.KeyEnv != "KENARI_KEY_BABU_SPARE_KEY" || k.Tier != "auto" || !k.Enabled {
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
	if r, _ := ae.do(t, ck, "POST", "/admin/api/keys", map[string]string{"client": "babu", "action": "add", "name": "spare-key", "key": "kn-x"}); r.StatusCode != 422 {
		t.Fatalf("duplicate key name accepted: %d", r.StatusCode)
	}
	_, b := ae.do(t, ck, "GET", "/admin/api/overview", nil)
	var ov struct {
		Alerts  []struct{ Level, Text string }
		Status  string
		Clients []struct {
			Name    string
			Keys    []struct{ Label string }
			Presets []struct {
				Name   string
				Models int
			}
		}
	}
	if err := json.Unmarshal(b, &ov); err != nil || ov.Status == "" || len(ov.Clients) != 4 {
		t.Fatalf("overview incomplete: %v %s", err, b)
	}
	if len(ov.Clients[0].Keys) != 3 || len(ov.Clients[0].Presets) == 0 || ov.Clients[0].Presets[0].Name != "text" || ov.Clients[0].Presets[0].Models == 0 {
		t.Fatalf("client row wrong: %+v", ov.Clients[0])
	}
	noKey := false
	for _, a := range ov.Alerts {
		if a.Level == "info" && strings.Contains(a.Text, "off has no kenari key") {
			noKey = true
		}
	}
	if !noKey {
		t.Fatalf("expected a hint for the client without keys: %+v", ov.Alerts)
	}
	if r, b := ae.do(t, ck, "POST", "/admin/api/keys", map[string]string{"client": "babu", "action": "remove", "name": "spare-key"}); r.StatusCode != 200 {
		t.Fatalf("remove: %d %s", r.StatusCode, b)
	}
	if _, ok := ae.a.Secrets()["KENARI_KEY_BABU_SPARE_KEY"]; ok {
		t.Fatal("removed key must also leave the secrets file")
	}
	if r, _ := ae.do(t, ck, "POST", "/admin/api/clients", map[string]string{"action": "delete", "name": "locked"}); r.StatusCode != 200 {
		t.Fatalf("client delete: %d", r.StatusCode)
	}
	if _, ok := ae.a.Secrets()["KEY_LOCKED"]; ok {
		t.Fatal("deleting a client must remove its kenari keys from the secrets file")
	}
	if r, b := ae.do(t, ck, "POST", "/admin/api/account-key", map[string]string{"key": "kn-account"}); r.StatusCode != 200 || ae.a.Secrets()["KENARI_ACCOUNT_KEY"] != "kn-account" {
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
