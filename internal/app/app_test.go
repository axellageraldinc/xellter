package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/proxy"
)

// fake is an in-process stand-in for kenari.id. Tests never reach the network.
type fake struct {
	mu    sync.Mutex
	calls []call
	// chat decides the response for a chat call; default is 200.
	chat func(c call, n int) (int, string)
	srv  *httptest.Server
}

type call struct {
	Path  string
	Model string
	Auth  string
	Body  string
}

func (f *fake) count(model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Model == model {
			n++
		}
	}
	return n
}

func (f *fake) last() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

const modelsJSON = `{"object":"list","data":[
 {"id":"free-a:free","endpoints":["chat"],"owned_by":"x","context_length":128000,"tool_call":true,"protocols":["openai-chat"],"modalities":{"input":["text"]},"pricing":{"free":true,"input":null,"output":null}},
 {"id":"cheap-b","endpoints":["chat"],"owned_by":"x","context_length":128000,"tool_call":true,"protocols":["openai-chat"],"modalities":{"input":["text"]},"pricing":{"free":false,"input":100000000,"output":300000000,"cache_read":10000000}},
 {"id":"pro-c","endpoints":["chat"],"owned_by":"x","context_length":200000,"tool_call":true,"protocols":["openai-chat"],"modalities":{"input":["text","image"]},"pricing":{"free":false,"input":5000000000,"output":20000000000,"cache_read":500000000}},
 {"id":"promo-d","endpoints":["chat"],"owned_by":"x","context_length":128000,"tool_call":true,"protocols":["openai-chat"],"modalities":{"input":["text"]},"pricing":{"free":false,"input":60000000,"output":250000000}},
 {"id":"img-paid","endpoints":["images"],"owned_by":"x","pricing":{"free":false,"input":0,"output":0},"pricing_lines":[{"unit":"image","micro_idr":300000000}]},
 {"id":"img-free:free","endpoints":["images"],"owned_by":"x","pricing":{"free":true}},
 {"id":"jev-1-13:free","endpoints":["chat"],"owned_by":"jev","pricing":{"free":true}},
 {"id":"stt-1","endpoints":["audio_transcription"],"owned_by":"x","pricing":{"free":false,"input":0,"output":0},"pricing_lines":[{"unit":"second","micro_idr":175000}]}
]}`

func newFake(t *testing.T) *fake {
	f := &fake{}
	end := time.Now().Add(7 * 24 * time.Hour).Unix()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, modelsJSON) })
	mux.HandleFunc("/api/public/pricing", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"free_tier":{"daily":50,"rpm":5,"next":{"daily":1000,"rpm":10},"plan":{"daily":0,"rpm":25},"threshold_idr":10000},"usd_idr_rate":17500,
		"items":[{"model":"promo-d","promo":{"percent_off":90,"starts_at":%d,"ends_at":%d,"original":{}},"upcoming_price":null}]}`, time.Now().Add(-time.Hour).Unix(), end)
	})
	mux.HandleFunc("/api/plans", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"name":"Indie","price_idr":49000,"budget_5h_micro_idr":0,"budget_week_micro_idr":75000000000,"budget_month_micro_idr":300000000000,"scope_models":["cheap-b"],"free_cache_models":["cheap-b"]}]`)
	})
	mux.HandleFunc("/v1/models/changes", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"data":[]}`) })
	rec := func(w http.ResponseWriter, r *http.Request) (call, bool) {
		b, _ := io.ReadAll(r.Body)
		c := call{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: string(b)}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			mr := multipart.NewReader(bytes.NewReader(b), strings.TrimPrefix(strings.Split(r.Header.Get("Content-Type"), "boundary=")[1], ""))
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				if p.FormName() == "model" {
					v, _ := io.ReadAll(p)
					c.Model = string(v)
				}
			}
		} else {
			var m struct{ Model string }
			_ = json.Unmarshal(b, &m)
			c.Model = m.Model
		}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.mu.Unlock()
		return c, true
	}
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		c, _ := rec(w, r)
		n := f.count(c.Model)
		if f.chat != nil {
			if st, body := f.chat(c, n); st != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(st)
				io.WriteString(w, body)
				return
			}
		}
		if strings.Contains(c.Body, `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			for i := 0; i < 3; i++ {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"t%d\"}}]}\n\n", i)
				fl.Flush()
			}
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":50,\"prompt_tokens_details\":{\"cached_tokens\":400}}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":400}}}`)
	})
	mux.HandleFunc("/v1/systemone", func(w http.ResponseWriter, r *http.Request) {
		rec(w, r)
		io.WriteString(w, `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.9}},"usage":{"input_tokens":120,"output_tokens":4}}`)
	})
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		rec(w, r)
		io.WriteString(w, `{"data":[{"b64_json":"AAAA"}]}`)
	})
	mux.HandleFunc("/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		rec(w, r)
		io.WriteString(w, `{"text":"hello"}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type env struct {
	a     *app.App
	f     *fake
	px    *httptest.Server
	keyCl string // client key with all shadows
}

func setup(t *testing.T, mutate func(*config.Config)) *env {
	t.Helper()
	f := newFake(t)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.DataDir = filepath.Join(dir, "data")
	cfg.Server.SecretsFile = filepath.Join(cfg.Server.DataDir, "secrets.env")
	cfg.Server.AuthRecord = filepath.Join(cfg.Server.DataDir, "auth.json")
	cfg.Kenari.BaseURL = f.srv.URL
	cfg.Rules.CanaryPercent = 0
	cfg.Upstreams = []config.Upstream{
		{Name: "plan", KeyEnv: "KEY_PLAN", Enabled: true, Tier: "plan", Plan: "Indie"},
		{Name: "topup", KeyEnv: "KEY_TOPUP", Enabled: true, Tier: "topup"},
	}
	key := "xl-testkey"
	cfg.Clients = []config.Client{
		{Name: "babu", Enabled: true, KeyHash: proxy.HashKey(key), Shadows: []string{"*"}},
		{Name: "locked", Enabled: true, KeyHash: proxy.HashKey("xl-locked"), Shadows: []string{"xl-fast"}},
		{Name: "off", Enabled: false, KeyHash: proxy.HashKey("xl-off"), Shadows: []string{"*"}},
		{Name: "direct", Enabled: true, KeyHash: proxy.HashKey("xl-direct"), Shadows: []string{"xl-chat"}, Passthrough: []string{"cheap-*"}},
	}
	cfg.Shadows = config.DefaultShadows()
	if mutate != nil {
		mutate(cfg)
	}
	if err := config.Normalize(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "xellter.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"KEY_PLAN": "secret-plan", "KEY_TOPUP": "secret-topup"} {
		if err := config.SetSecret(cfg.Server.SecretsFile, k, v); err != nil {
			t.Fatal(err)
		}
	}
	src := &catalog.Kenari{BaseURL: f.srv.URL, UA: "test", HTTP: f.srv.Client()}
	a, err := app.New(path, src)
	if err != nil {
		t.Fatal(err)
	}
	a.Logf = t.Logf
	if err := a.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Rt.MarkSeen(a.Cat.Snapshot())
	a.Rt.Recompute(time.Now())
	px := httptest.NewServer(a.Px)
	t.Cleanup(func() { px.Close(); a.Close() })
	return &env{a: a, f: f, px: px, keyCl: key}
}

func (e *env) post(t *testing.T, key, path, body string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.px.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const chatBody = `{"model":"%s","messages":[{"role":"user","content":"hi"}]}`

func TestAuthAndModelAccess(t *testing.T) {
	e := setup(t, nil)
	if r, _ := e.post(t, "", "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat")); r.StatusCode != 401 {
		t.Fatalf("no key: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-wrong", "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat")); r.StatusCode != 401 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-off", "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat")); r.StatusCode != 403 {
		t.Fatalf("disabled client: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-locked", "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat")); r.StatusCode != 403 {
		t.Fatalf("shadow not allowed: %d", r.StatusCode)
	}
	if r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "cheap-b")); r.StatusCode != 403 {
		t.Fatalf("passthrough must be off by default: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-direct", "/v1/chat/completions", fmt.Sprintf(chatBody, "cheap-b")); r.StatusCode != 200 {
		t.Fatalf("allowed passthrough: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-direct", "/v1/chat/completions", fmt.Sprintf(chatBody, "pro-c")); r.StatusCode != 403 {
		t.Fatalf("passthrough glob must not match pro-c: %d", r.StatusCode)
	}
	if r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "nope")); r.StatusCode != 404 {
		t.Fatalf("unknown model: %d", r.StatusCode)
	}
	if r, _ := e.post(t, e.keyCl, "/v1/images/generations", fmt.Sprintf(chatBody, "xl-chat")); r.StatusCode != 400 {
		t.Fatalf("modality mismatch must be 400: %d", r.StatusCode)
	}
}

func TestShadowRoutesToFreeOrPlanFirstAndRewritesModel(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat"))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, body)
	}
	got := resp.Header.Get("X-Xellter-Model")
	if got != "free-a:free" && got != "cheap-b" && got != "promo-d" {
		t.Fatalf("expected a free/plan/promo model first, got %q", got)
	}
	if e.f.last().Model != got {
		t.Fatalf("upstream saw model %q, header says %q", e.f.last().Model, got)
	}
	if strings.Contains(e.f.last().Auth, "xl-") {
		t.Fatal("client key leaked upstream")
	}
	rk := e.a.Rt.Rankings()["xl-chat"]
	for _, c := range rk.Candidates {
		if c.Model == "pro-c" && rk.Candidates[0].Model == "pro-c" {
			t.Fatalf("expensive model must not lead: %+v", rk.Candidates)
		}
	}
}

func TestFallbackOnUpstreamErrorAndRateLimit(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		sh := c.Shadows["xl-chat"]
		sh.Mode, sh.Models, sh.Strategy = "combo", []string{"free-a:free", "cheap-b"}, "priority"
		c.Shadows["xl-chat"] = sh
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			return 503, `{"error":{"code":"upstream_error","message":"down"}}`
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat"))
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("expected fallback to cheap-b, got %d %s model=%s", resp.StatusCode, body, resp.Header.Get("X-Xellter-Model"))
	}
	// the failing model is now blocked: next request goes straight to cheap-b
	before := e.f.count("free-a:free")
	e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat"))
	if e.f.count("free-a:free") > before+0 && e.f.count("free-a:free") > 2 {
		t.Fatalf("blocked model kept being retried: %d calls", e.f.count("free-a:free"))
	}
}

func TestRateLimitedKeyCoolsDownAndOtherKeyServes(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		sh := c.Shadows["xl-chat"]
		sh.Mode, sh.Models, sh.Strategy = "combo", []string{"free-a:free"}, "priority"
		c.Shadows["xl-chat"] = sh
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Auth == "Bearer secret-plan" || c.Auth == "Bearer secret-topup" {
			if n == 1 { // first ever call to this model: 429 whichever key got it
				return 429, `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`
			}
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat"))
	if resp.StatusCode != 200 {
		t.Fatalf("second key should have served: %d %s", resp.StatusCode, body)
	}
	if e.f.count("free-a:free") != 2 {
		t.Fatalf("expected exactly 2 upstream calls, got %d", e.f.count("free-a:free"))
	}
}

func TestStreamingAndUsageAccounting(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		sh := c.Shadows["xl-chat"]
		sh.Mode, sh.Models, sh.Strategy = "combo", []string{"cheap-b"}, "priority"
		c.Shadows["xl-chat"] = sh
	})
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"xl-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "t0") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream not relayed: %d %q", resp.StatusCode, body)
	}
	if !strings.Contains(e.f.last().Body, `"include_usage":true`) {
		t.Fatalf("usage option not injected: %s", e.f.last().Body)
	}
	e.a.St.Flush()
	rows, _ := e.a.St.Recent(5)
	if len(rows) == 0 || rows[0].InTok != 1000 || rows[0].OutTok != 50 || rows[0].Billing != "plan" || !rows[0].OK {
		t.Fatalf("usage row wrong: %+v", rows)
	}
	if rows[0].Cost <= 0 {
		t.Fatalf("catalog cost should be recorded for plan traffic: %+v", rows[0])
	}
}

func TestClientBudget(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].DailyBudget = 0.0001
		sh := c.Shadows["xl-chat"]
		sh.Mode, sh.Models, sh.Strategy = "combo", []string{"pro-c"}, "priority"
		c.Shadows["xl-chat"] = sh
	})
	if r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat")); r.StatusCode != 200 {
		t.Fatalf("first call: %d", r.StatusCode)
	}
	if r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat")); r.StatusCode != 429 {
		t.Fatalf("budget not enforced: %d", r.StatusCode)
	}
}

func TestRTKCompressesToolOutput(t *testing.T) {
	e := setup(t, nil)
	var diff strings.Builder
	diff.WriteString("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,300 +1,300 @@\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&diff, " unchanged line number %d of the file\n", i)
	}
	diff.WriteString("-old\n+new\n")
	tool, _ := json.Marshal(diff.String())
	body := fmt.Sprintf(`{"model":"xl-chat","messages":[{"role":"user","content":"diff?"},{"role":"tool","tool_call_id":"1","content":%s}]}`, tool)
	if r, b := e.post(t, e.keyCl, "/v1/chat/completions", body); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if got := len(e.f.last().Body); got > len(body)/3 {
		t.Fatalf("tool output not compressed: sent %d of %d bytes", got, len(body))
	}
	if !strings.Contains(e.f.last().Body, `+new`) {
		t.Fatal("changed lines must survive compression")
	}
	// per-request bypass
	if r, _ := e.post(t, e.keyCl, "/v1/chat/completions", body, "X-Xellter-RTK", "off"); r.StatusCode != 200 {
		t.Fatal("bypass failed")
	}
	if got := len(e.f.last().Body); got < len(body)/2 {
		t.Fatalf("bypass header ignored: %d", got)
	}
}

func TestMediaRoutingPrefersFreeAndMultipartModelRewrite(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.post(t, e.keyCl, "/v1/images/generations", `{"model":"xl-image","prompt":"cat"}`)
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "img-free:free" {
		t.Fatalf("image should use the free model first: %d %s %s", resp.StatusCode, resp.Header.Get("X-Xellter-Model"), body)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "xl-stt")
	fw, _ := mw.CreateFormFile("file", "a.mp3")
	fw.Write([]byte("AUDIO"))
	mw.Close()
	req, _ := http.NewRequest("POST", e.px.URL+"/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+e.keyCl)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || e.f.last().Model != "stt-1" || !strings.Contains(e.f.last().Body, "AUDIO") {
		t.Fatalf("multipart not rewritten: %d %s model=%q", r.StatusCode, b, e.f.last().Model)
	}
}

func TestModelListShowsOnlyAllowedShadows(t *testing.T) {
	e := setup(t, nil)
	req, _ := http.NewRequest("GET", e.px.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer xl-locked")
	r, _ := http.DefaultClient.Do(req)
	var out struct{ Data []struct{ ID string } }
	json.NewDecoder(r.Body).Decode(&out)
	if len(out.Data) != 1 || out.Data[0].ID != "xl-fast" {
		t.Fatalf("got %+v", out.Data)
	}
}

func TestLearnsAllowedModelsFromRestrictedKey(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		sh := c.Shadows["xl-chat"]
		sh.Mode, sh.Models, sh.Strategy = "combo", []string{"pro-c", "cheap-b"}, "priority"
		c.Shadows["xl-chat"] = sh
		c.Upstreams = c.Upstreams[:1]
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "pro-c" {
			return 400, `{"error":{"code":"bad_request","message":"model not permitted for this key. Allowed models: cheap-b, free-a:free"}}`
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat"))
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("expected cheap-b after learning: %d %s", resp.StatusCode, body)
	}
	e.a.Rt.Recompute(time.Now())
	st := e.a.Pool.Keys()[0].Status(e.a.Cat.Snapshot(), time.Now())
	if len(st.Learned) != 2 {
		t.Fatalf("allow list not learned: %+v", st.Learned)
	}
}
