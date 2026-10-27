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
	// account answers /v1/account/quota; default: 403 like a restricted key.
	account func(auth string) (int, string)
	srv     *httptest.Server
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
 {"id":"stt-1","endpoints":["audio_transcription"],"owned_by":"x","pricing":{"free":false,"input":0,"output":0},"pricing_lines":[{"unit":"second","micro_idr":175000}]}
]}`

func newFake(t testing.TB) *fake {
	f := &fake{}
	end := time.Now().Add(7 * 24 * time.Hour).Unix()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, modelsJSON)
	})
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
		if c.Model == "xellter-probe" { // kenari rejects unknown models; without an allow list the key is unrestricted
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":"model_not_found","message":"no such model"}}`)
			return
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
	mux.HandleFunc("/v1/account/quota", func(w http.ResponseWriter, r *http.Request) {
		st, body := 403, `{"error":{"code":"restricted_key_not_allowed","message":"restricted"}}`
		if f.account != nil {
			st, body = f.account(r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		io.WriteString(w, body)
	})
	mux.HandleFunc("/v1/account/balance", func(w http.ResponseWriter, r *http.Request) {
		if f.account == nil {
			w.WriteHeader(403)
			return
		}
		io.WriteString(w, `{"object":"account.balance","balance_micro_idr":25000000000,"reserved_micro_idr":1500000,"available_micro_idr":24998500000,"currency":"IDR"}`)
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
	keyCl string // client key with all presets
}

func setup(t testing.TB, mutate func(*config.Config)) *env {
	t.Helper()
	f := newFake(t)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.DataDir = filepath.Join(dir, "data")
	cfg.Server.SecretsFile = filepath.Join(cfg.Server.DataDir, "secrets.env")
	cfg.Server.AuthRecord = filepath.Join(cfg.Server.DataDir, "auth.json")
	cfg.Kenari.BaseURL = f.srv.URL
	key := "xl-testkey"
	one := func(env string) []config.Upstream { return []config.Upstream{{Name: "k", KeyEnv: env}} }
	// babu: every category, all models, so a test can drive any endpoint.
	all := map[string]config.Preset{
		"text":  {Category: "chat", Optimization: "cheapest", Models: []string{"free-a:free", "cheap-b", "pro-c", "promo-d"}},
		"image": {Category: "images", Optimization: "cheapest", Models: []string{"img-paid", "img-free:free"}},
		"stt":   {Category: "audio_transcription", Optimization: "cheapest", Models: []string{"stt-1"}},
	}
	cfg.Clients = []config.Client{
		{Name: "babu", Enabled: true, Purpose: "general", KeyHash: proxy.HashKey(key), KeyHint: "…babu",
			Keys:    []config.Upstream{{Name: "plan", KeyEnv: "KEY_PLAN"}, {Name: "topup", KeyEnv: "KEY_TOPUP"}},
			Presets: all},
		{Name: "locked", Enabled: true, Purpose: "general", KeyHash: proxy.HashKey("xl-locked"), Keys: one("KEY_LOCKED"),
			Presets: map[string]config.Preset{"text": {Category: "chat", Optimization: "cheapest", Models: []string{"cheap-b"}}}},
		{Name: "off", Enabled: false, Purpose: "general", KeyHash: proxy.HashKey("xl-off"), Presets: all},
		// direct: one model in its pool, so naming that model directly is allowed and nothing else is
		{Name: "direct", Enabled: true, Purpose: "general", KeyHash: proxy.HashKey("xl-direct"), Keys: one("KEY_DIRECT"),
			Presets: map[string]config.Preset{"text": {Category: "chat", Optimization: "cheapest", Models: []string{"cheap-b"}}}},
		{Name: "coded", Enabled: true, Purpose: "coding", KeyHash: proxy.HashKey("xl-coded"), Keys: one("KEY_CODED"),
			Presets: map[string]config.Preset{"text": {Category: "chat", Optimization: "balanced", Models: []string{"cheap-b", "pro-c", "promo-d"}}}},
	}
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
	for k, v := range map[string]string{"KEY_PLAN": "secret-plan", "KEY_TOPUP": "secret-topup", "KEY_LOCKED": "secret-locked", "KEY_DIRECT": "secret-direct", "KEY_CODED": "secret-coded", "KENARI_ACCOUNT_KEY": "secret-account"} {
		if err := config.SetSecret(cfg.Server.SecretsFile, k, v); err != nil {
			t.Fatal(err)
		}
	}
	src := &catalog.Kenari{BaseURL: f.srv.URL, UA: "test", HTTP: f.srv.Client()}
	a, err := app.New(path, src)
	if err != nil {
		t.Fatal(err)
	}
	// Background probes keep logging after a test ends; a Logf that guards against that keeps `go test -race`
	// from panicking on a log line that arrives late.
	var logMu sync.Mutex
	done := false
	a.Logf = func(f string, v ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		if done {
			return
		}
		t.Logf(f, v...)
	}
	t.Cleanup(func() { logMu.Lock(); done = true; logMu.Unlock() })
	if err := a.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
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
	if r, _ := e.post(t, "", "/v1/chat/completions", fmt.Sprintf(chatBody, "text")); r.StatusCode != 401 {
		t.Fatalf("no key: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-wrong", "/v1/chat/completions", fmt.Sprintf(chatBody, "text")); r.StatusCode != 401 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-off", "/v1/chat/completions", fmt.Sprintf(chatBody, "text")); r.StatusCode != 403 {
		t.Fatalf("disabled client: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-locked", "/v1/chat/completions", fmt.Sprintf(chatBody, "image")); r.StatusCode != 404 {
		t.Fatalf("a client only knows its own presets: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-direct", "/v1/chat/completions", fmt.Sprintf(chatBody, "cheap-b")); r.StatusCode != 200 {
		t.Fatalf("a model in the client's own pool must be callable by name: %d", r.StatusCode)
	}
	if r, _ := e.post(t, "xl-direct", "/v1/chat/completions", fmt.Sprintf(chatBody, "pro-c")); r.StatusCode != 403 {
		t.Fatalf("a model outside every pool must be refused: %d", r.StatusCode)
	}
	if r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "nope")); r.StatusCode != 404 {
		t.Fatalf("unknown model: %d", r.StatusCode)
	}
	if r, _ := e.post(t, e.keyCl, "/v1/images/generations", fmt.Sprintf(chatBody, "text")); r.StatusCode != 400 {
		t.Fatalf("modality mismatch must be 400: %d", r.StatusCode)
	}
}

func TestPresetRoutesToFreeOrPlanFirstAndRewritesModel(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
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
	// the ranking explains the choice: a free model leads a cheapest preset, and the dearest model is last
	rk := e.a.Rt.Rankings()["babu/text"]
	if len(rk.Candidates) == 0 {
		t.Fatal("the preset must have a ranking")
	}
	if rk.Candidates[0].Model != "free-a:free" {
		t.Fatalf("a cheapest preset must lead with the free model: %+v", rk.Candidates)
	}
	if last := rk.Candidates[len(rk.Candidates)-1].Model; last != "pro-c" {
		t.Fatalf("the dearest model must come last: %+v", rk.Candidates)
	}
}

// A model that fails is blocked on the key that saw the failure, so the next request does not pay for the
// same discovery again.
func TestAFailingModelIsSkippedAfterwards(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"free-a:free", "cheap-b"}}
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			return 503, `{"error":{"code":"upstream_error","message":"down"}}`
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("expected fallback to cheap-b, got %d %s model=%s", resp.StatusCode, body, resp.Header.Get("X-Xellter-Model"))
	}
	if n := e.f.count("free-a:free"); n != 2 {
		t.Fatalf("the failing model gets one try and one retry, saw %d", n)
	}
	// the failure blocked the model on both keys of the account, so the next session skips it entirely
	resp, body = e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"), "X-Xellter-Session", "s2")
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("a blocked model must be skipped: %d %s", resp.StatusCode, body)
	}
	if n := e.f.count("free-a:free"); n != 2 {
		t.Fatalf("a blocked model must not be tried again, saw %d calls", n)
	}
}

// kenari limits per account and there is only one account, so a rate limit on one key of a client blocks the
// other key too. The retry of requirement 15 does not change that: it is the same account being limited.
func TestRateLimitCooldownIsAccountWide(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"free-a:free"}}
	})
	e.f.chat = func(c call, n int) (int, string) {
		return 429, `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if resp.StatusCode != 429 {
		t.Fatalf("the account is rate limited, got %d %s", resp.StatusCode, body)
	}
	// one attempt plus its one retry, and the second key of the client is not used: the limit is account wide
	if n := e.f.count("free-a:free"); n != 2 {
		t.Fatalf("expected 2 upstream calls (one try plus one retry), got %d", n)
	}
	m := e.a.Cat.Snapshot().Models["free-a:free"]
	for _, k := range e.a.Pool.Keys() {
		if why := k.Check(m, e.a.Cat.Snapshot(), time.Now()); why != "cooldown" {
			t.Fatalf("key %s must be cooling down too, got %q", k.Name, why)
		}
	}
	// a new session cannot be served either: no key may take it
	resp, _ = e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"), "X-Xellter-Session", "s2")
	if resp.StatusCode != 429 {
		t.Fatalf("a cooling account must refuse a new session: %d", resp.StatusCode)
	}
	if n := e.f.count("free-a:free"); n != 2 {
		t.Fatalf("a cooling account must not reach kenari again: %d calls", n)
	}
}

// Requirement 15: a retriable error retries the same model once, and only then moves to the next model.
func TestRetriableErrorRetriesTheSameModelOnce(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"free-a:free", "cheap-b"}}
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			return 503, `{"error":{"code":"upstream_error","message":"down"}}`
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("expected the next model: %d %s model=%s", resp.StatusCode, body, resp.Header.Get("X-Xellter-Model"))
	}
	// the first model was tried exactly twice: once, then its one retry
	if n := e.f.count("free-a:free"); n != 2 {
		t.Fatalf("a retriable error must be retried exactly once, saw %d attempts", n)
	}
	if got := resp.Header.Get("X-Xellter-Attempt"); got != "3" {
		t.Fatalf("the attempt number must be reported: %q", got)
	}
}

// A non-retriable error is not retried on the same model: the next model takes over at once.
func TestNonRetriableErrorMovesOnAtOnce(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"free-a:free", "cheap-b"}}
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			return 400, `{"error":{"code":"bad_request","message":"messages is required"}}`
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("expected the next model: %d %s model=%s", resp.StatusCode, body, resp.Header.Get("X-Xellter-Model"))
	}
	if n := e.f.count("free-a:free"); n != 1 {
		t.Fatalf("a client error must not be retried, saw %d attempts", n)
	}
}

func TestThreeErrorsEvictTheModelFromThePreset(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"free-a:free", "cheap-b"}}
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			return 400, `{"error":{"code":"bad_request","message":"messages is required"}}`
		}
		return 0, ""
	}
	// two errors are already on record, so the next failure is the third and evicts
	e.a.Rt.Failed("babu", "text", "free-a:free", time.Now().Add(-2*time.Minute))
	e.a.Rt.Failed("babu", "text", "free-a:free", time.Now().Add(-time.Minute))

	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"), "X-Xellter-Session", "s1")
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("the failing model must fall back: %d %s model=%s", resp.StatusCode, body, resp.Header.Get("X-Xellter-Model"))
	}
	ev := e.a.Rt.Evictions("babu", "text", time.Now())
	if _, ok := ev["free-a:free"]; !ok {
		t.Fatalf("the third error must evict the model: %v", ev)
	}

	// the next request goes straight to the replacement
	before := e.f.count("free-a:free")
	resp, _ = e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"), "X-Xellter-Session", "s2")
	if resp.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("an evicted model must be skipped: %s", resp.Header.Get("X-Xellter-Model"))
	}
	if n := e.f.count("free-a:free"); n != before {
		t.Fatalf("an evicted model must not be tried again: %d -> %d", before, n)
	}

	// eviction is routing state, not a re-ranking: the model is still in the list
	rk := e.a.Rt.Rankings()["babu/text"]
	found := false
	for _, c := range rk.Candidates {
		if c.Model == "free-a:free" {
			found = true
		}
	}
	if !found {
		t.Fatalf("eviction must not remove the model from the ranking: %+v", rk.Candidates)
	}
}

func TestStreamingAndUsageAccounting(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"cheap-b"}}
	})
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "t0") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream not relayed: %d %q", resp.StatusCode, body)
	}
	if !strings.Contains(e.f.last().Body, `"include_usage":true`) {
		t.Fatalf("usage option not injected: %s", e.f.last().Body)
	}
	e.a.St.Flush()
	rows, _ := e.a.St.Recent(5, "")
	// A free model is in the pool of every preset (ADR 0022), so the cheapest preset
	// serves it: the traffic is free, not payg.
	if len(rows) == 0 || rows[0].InTok != 1000 || rows[0].OutTok != 50 || rows[0].Billing != "free" || !rows[0].OK {
		t.Fatalf("usage row wrong: %+v", rows)
	}
	if rows[0].Cost != 0 {
		t.Fatalf("a free model costs nothing: %+v", rows[0])
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
	body := fmt.Sprintf(`{"model":"text","messages":[{"role":"user","content":"diff?"},{"role":"tool","tool_call_id":"1","content":%s}]}`, tool)
	if r, b := e.post(t, e.keyCl, "/v1/chat/completions", body); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if got := len(e.f.last().Body); got > len(body)/3 {
		t.Fatalf("tool output not compressed: sent %d of %d bytes", got, len(body))
	}
	if !strings.Contains(e.f.last().Body, `+new`) {
		t.Fatal("changed lines must survive compression")
	}
}

func TestMediaRoutingPrefersFreeAndMultipartModelRewrite(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.post(t, e.keyCl, "/v1/images/generations", `{"model":"image","prompt":"cat"}`)
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "img-free:free" {
		t.Fatalf("image should use the free model first: %d %s %s", resp.StatusCode, resp.Header.Get("X-Xellter-Model"), body)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "stt")
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

func TestModelListShowsOnlyAllowedPresets(t *testing.T) {
	e := setup(t, nil)
	req, _ := http.NewRequest("GET", e.px.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer xl-locked")
	r, _ := http.DefaultClient.Do(req)
	var out struct{ Data []struct{ ID string } }
	json.NewDecoder(r.Body).Decode(&out)
	// The preset, the model in its pool, and the free and discounted models xellter
	// added on its own (ADR 0022). A client may name any of them.
	want := []string{"text", "cheap-b", "free-a:free", "promo-d"}
	if len(out.Data) != len(want) {
		t.Fatalf("got %+v, want %v", out.Data, want)
	}
	for i, id := range want {
		if out.Data[i].ID != id {
			t.Fatalf("got %+v, want %v", out.Data, want)
		}
	}
}

func TestLearnsAllowedModelsFromRestrictedKey(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"pro-c", "cheap-b"}}
		c.Clients[0].Keys = c.Clients[0].Keys[:1]
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" { // the model the cheapest preset serves first is not allowed on this key
			return 400, `{"error":{"code":"bad_request","message":"model not permitted for this key. Allowed models: pro-c, free-a:free"}}`
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if resp.StatusCode != 200 {
		t.Fatalf("expected a served model after learning: %d %s", resp.StatusCode, body)
	}
	// The key may call pro-c and free-a:free, so one of those is served.
	if got := resp.Header.Get("X-Xellter-Model"); got != "free-a:free" && got != "pro-c" {
		t.Fatalf("expected a model the key may call, got %q", got)
	}
	e.a.Rt.Recompute(time.Now())
	st := e.a.Pool.Keys()[0].Status(time.Now())
	if len(st.Learned) != 2 {
		t.Fatalf("allow list not learned: %+v", st.Learned)
	}
}
