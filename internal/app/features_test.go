package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/proxy"
	"github.com/axellageraldinc/xellter/internal/store"
)

func twoEqualModels(c *config.Config) {
	c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"cheap-b", "pro-c"}}
}

func TestRoundRobinRotatesAndSessionsStick(t *testing.T) {
	e := setup(t, twoEqualModels)
	seen := map[string]int{}
	for i := 0; i < 6; i++ { // different prompts = different sessions (identical prompts stick to one model)
		r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(`{"model":"text","messages":[{"role":"user","content":"question %d"}]}`, i))
		seen[r.Header.Get("X-Xellter-Model")]++
	}
	if seen["cheap-b"] == 0 || seen["pro-c"] == 0 {
		t.Fatalf("round robin must use both models: %v", seen)
	}
	// with a session header, every request of the session lands on the same model
	first := ""
	for i := 0; i < 6; i++ {
		r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"), "X-Xellter-Session", "s1")
		m := r.Header.Get("X-Xellter-Model")
		if first == "" {
			first = m
		}
		if m != first {
			t.Fatalf("session moved from %s to %s", first, m)
		}
	}
}

// A model that fails falls back to the next one in the list.
func TestAFailingModelFallsBackToTheNext(t *testing.T) {
	e := setup(t, nil)
	rk := e.a.Rt.Rankings()["babu/text"]
	if len(rk.Candidates) < 2 {
		t.Fatalf("the preset needs more than one candidate: %+v", rk)
	}
	first := rk.Candidates[0].Model
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == first {
			return 503, `{"error":{"message":"boom"}}`
		}
		return 0, ""
	}
	e.a.Pool.Keys()[0].ResetState()
	e.a.Pool.Keys()[1].ResetState()
	r, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if r.StatusCode != 200 || r.Header.Get("X-Xellter-Model") == first {
		t.Fatalf("a failing model must fall back: %d %s %s", r.StatusCode, r.Header.Get("X-Xellter-Model"), body)
	}
}

func TestProbeLearnsAllowListWithoutBilling(t *testing.T) {
	e := setup(t, nil)
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "xellter-probe" && c.Auth == "Bearer secret-plan" {
			return 400, `{"error":{"code":"bad_request","message":"model 'xellter-probe' is not allowed by this key. Allowed models: cheap-b, free-a:free"}}`
		}
		return 404, `{"error":{"code":"model_not_found","message":"no such model"}}`
	}
	e.a.ProbeKeys(context.Background())
	var plan, topup []string
	for _, k := range e.a.Pool.Keys() {
		st := k.Status(time.Now())
		if st.Client == "babu" && st.Label == "plan" {
			plan = st.Learned
		} else {
			topup = st.Learned
		}
	}
	if len(plan) != 2 || len(topup) != 0 {
		t.Fatalf("plan=%v topup=%v", plan, topup)
	}
	for _, c := range e.a.Rt.Rankings()["babu/text"].Candidates {
		if c.Model == "pro-c" { // topup (unrestricted) can still serve it
			return
		}
	}
	t.Fatal("pro-c should remain routable through the unrestricted key")
}

// A client may hold a key dedicated to one service (the X search). Such a key is
// never probed and never serves a model call: it is pay-as-you-go, and xellter must
// not spend it on its own curiosity. The search endpoint uses it, and only it.
func TestSearchKeyIsNeverProbedAndNeverServesAModel(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Keys = append(c.Clients[0].Keys, config.Upstream{Name: "X", KeyEnv: "KEY_X", Capability: "search"})
	})
	// The secret exists, so the key is in the pool.
	e.a.Pool.Reload(e.a.Cfg(), map[string]string{
		"KEY_PLAN": "secret-plan", "KEY_TOPUP": "secret-topup", "KEY_LOCKED": "secret-locked",
		"KEY_DIRECT": "secret-direct", "KEY_CODED": "secret-coded", "KENARI_ACCOUNT_KEY": "secret-account",
		"KEY_X": "secret-x",
	}, nil)

	// The probe asks every key for a chat model. The search key must not be asked.
	e.a.ProbeKeys(context.Background())
	for _, c := range e.f.calls {
		if c.Auth == "Bearer secret-x" {
			t.Fatalf("the search key was probed: %+v", c)
		}
	}

	// A model call must never land on the search key either.
	e.f.calls = nil
	r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if r.StatusCode != 200 {
		t.Fatalf("the chat call failed: %d", r.StatusCode)
	}
	for _, c := range e.f.calls {
		if c.Auth == "Bearer secret-x" {
			t.Fatalf("the search key served a model call: %+v", c)
		}
	}

	// The search endpoint uses that key, and only that key.
	e.f.calls = nil
	r, body := e.post(t, e.keyCl, "/v1/x/search", `{"query":"news"}`)
	if r.StatusCode != 200 {
		t.Fatalf("the search call failed: %d %s", r.StatusCode, body)
	}
	if len(e.f.calls) != 1 || e.f.calls[0].Auth != "Bearer secret-x" {
		t.Fatalf("the search must go to the search key: %+v", e.f.calls)
	}
	if !strings.Contains(body, "@someone") {
		t.Fatalf("the answer must be the upstream's: %s", body)
	}
}

// A client without a search key is told so, instead of a model call being forwarded.
func TestSearchWithoutAKeyIsRefused(t *testing.T) {
	e := setup(t, nil)
	r, body := e.post(t, e.keyCl, "/v1/x/search", `{"query":"news"}`)
	if r.StatusCode != 404 {
		t.Fatalf("status = %d, want 404: %s", r.StatusCode, body)
	}
}

// A preset must not answer an endpoint xellter does not serve. A chat preset used to
// answer any path, so a request for an unknown endpoint was forwarded as a model call
// and its failure was counted against every model of the preset.
func TestUnknownEndpointDoesNotReachTheModel(t *testing.T) {
	e := setup(t, nil)
	e.f.calls = nil
	r, body := e.post(t, e.keyCl, "/v1/not-an-endpoint", fmt.Sprintf(chatBody, "text"))
	if r.StatusCode != 404 {
		t.Fatalf("status = %d, want 404: %s", r.StatusCode, body)
	}
	if len(e.f.calls) != 0 {
		t.Fatalf("nothing must reach the upstream: %+v", e.f.calls)
	}
}

func TestAccountQuotaFromUnrestrictedKeyIsRead(t *testing.T) {
	e := setup(t, nil)
	reset := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	e.f.account = func(auth string) (int, string) {
		if auth != "Bearer secret-topup" {
			return 403, `{"error":{"code":"restricted_key_not_allowed","message":"restricted"}}`
		}
		return 200, `{"plan":{"name":"Indie","windows":{"week":{"budget_rp":1000,"used_rp":990,"remaining_rp":10,"used_frac":0.99,"resets_at":"` + reset + `"}}},"coupon":null,"alert_pct":80}`
	}
	e.a.ProbeKeys(context.Background())
	st := e.a.Pool.Accounts()[0].Status(e.a.Cat.Snapshot(), time.Now())
	if st.Plan != "Indie" || st.PlanSource != "kenari" || st.QuotaKey != "babu/topup" || st.Windows["week"].UsedRP != 990 {
		t.Fatalf("account not read from kenari: %+v", st)
	}
	if st.BalanceIDR != 24998.5 {
		t.Fatalf("balance %v", st.BalanceIDR)
	}
	// kenari says the weekly budget is 99% used, so plan models stop counting as covered
	m := e.a.Cat.Snapshot().Models["cheap-b"]
	if e.a.Pool.Keys()[0].Covered(m, e.a.Cat.Snapshot(), time.Now()) {
		t.Fatal("exhausted weekly window must uncover plan models")
	}
	// plan limit is account wide: a 429 on one key blocks the other key of the account too
	e.a.Pool.Keys()[0].Failure("cheap-b", 429, []byte(`{"error":{"code":"rate_limit_exceeded"}}`), 0, time.Now())
	if r := e.a.Pool.Keys()[1].Check(m, e.a.Cat.Snapshot(), time.Now()); r != "cooldown" {
		t.Fatalf("rate limits are per account, got %q", r)
	}
}

func TestFreeQuotaErrorsAreAccountWide(t *testing.T) {
	e := setup(t, nil)
	free := e.a.Cat.Snapshot().Models["free-a:free"]
	paid := e.a.Cat.Snapshot().Models["pro-c"]
	k0, k1 := e.a.Pool.Keys()[0], e.a.Pool.Keys()[1]
	now := time.Now()
	k0.Failure("free-a:free", 429, []byte(`{"error":{"code":"free_quota_daily","message":"used"}}`), 0, now)
	if r := k1.Check(free, e.a.Cat.Snapshot(), now); r != "daily free quota used" {
		t.Fatalf("free daily quota is per account, got %q", r)
	}
	if r := k1.Check(paid, e.a.Cat.Snapshot(), now); r != "" {
		t.Fatalf("paid models must stay usable: %q", r)
	}
}

// Presets are not generated any more: probing a key learns its allow list, and the client's presets stay
// exactly what the owner configured (ADR 0019).
func TestProbingDoesNotGeneratePresets(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients = append(c.Clients, config.Client{Name: "gen", Enabled: true, KeyHash: proxy.HashKey("xl-gen"),
			Keys: []config.Upstream{{Name: "limited", KeyEnv: "KEY_GEN"}}})
	})
	if err := config.SetSecret(e.a.Cfg().Server.SecretsFile, "KEY_GEN", "secret-gen"); err != nil {
		t.Fatal(err)
	}
	e.a.Pool.Reload(e.a.Cfg(), e.a.Secrets(), e.a.St)
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "xellter-probe" && c.Auth == "Bearer secret-gen" {
			return 400, `{"error":{"code":"bad_request","message":"model 'xellter-probe' is not allowed by this key. Allowed models: cheap-b, free-a:free"}}`
		}
		return 0, ""
	}
	e.a.ProbeKeys(context.Background())
	for _, c := range e.a.Cfg().Clients {
		if c.Name == "gen" && len(c.Presets) != 0 {
			t.Fatalf("no preset may be created automatically: %v", c.Presets)
		}
	}
	// the learned allow list is still used: a model the key cannot call is not offered
	if got := e.a.Pool.Get("gen/limited"); got == nil {
		t.Fatal("the key is missing")
	} else if !got.AllowedModel("cheap-b") || got.AllowedModel("pro-c") {
		t.Fatalf("allow list not learned: %+v", got.Status(time.Now()).Learned)
	}
}

func TestClientsNeverSeeEachOthersModels(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		pool := []string{"cheap-b", "pro-c", "promo-d"}
		for _, n := range []string{"one", "two"} {
			c.Clients = append(c.Clients, config.Client{Name: n, Enabled: true, Purpose: "general", KeyHash: proxy.HashKey("xl-" + n),
				Presets: map[string]config.Preset{"text": {Category: "chat", Optimization: "cheapest", Models: pool}},
				Keys:    []config.Upstream{{Name: "k", KeyEnv: "KEY_" + strings.ToUpper(n)}}})
		}
	})
	for _, n := range []string{"one", "two"} {
		if err := config.SetSecret(e.a.Cfg().Server.SecretsFile, "KEY_"+strings.ToUpper(n), "secret-"+n); err != nil {
			t.Fatal(err)
		}
	}
	e.a.Pool.Reload(e.a.Cfg(), e.a.Secrets(), e.a.St)
	// the keys are restricted to different models; kenari tells xellter which ones
	e.a.Pool.Get("one/k").SetAllowed([]string{"cheap-b"})
	e.a.Pool.Get("two/k").SetAllowed([]string{"pro-c", "promo-d"})
	e.a.Rt.Recompute(time.Now())
	models := func(client string) []string {
		rk := e.a.Rt.Rankings()[client+"/text"]
		if rk == nil {
			t.Fatalf("no ranking for %s", client)
		}
		var out []string
		for _, c := range rk.Candidates {
			out = append(out, c.Model)
		}
		return out
	}
	if got := models("one"); len(got) != 1 || got[0] != "cheap-b" {
		t.Fatalf("client one may only reach cheap-b, got %v", got)
	}
	if got := models("two"); len(got) != 2 {
		t.Fatalf("client two may only reach pro-c and promo-d, got %v", got)
	}
	// and the request path agrees: client one cannot reach a model of client two
	r, _ := e.post(t, "xl-one", "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}]}`)
	if r.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("served by %q", r.Header.Get("X-Xellter-Model"))
	}
}

// Quality comes from the published leaderboards, fetched and stored, never typed into the config.
func TestLeaderboardScoresFeedTheRanking(t *testing.T) {
	e := setup(t, nil)
	hf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		row := func(name string, rating float64) map[string]any {
			return map[string]any{"row": map[string]any{"model_name": name, "rating": rating, "rating_lower": rating - 5, "rating_upper": rating + 5, "vote_count": 500, "category": "overall", "leaderboard_publish_date": "2026-10-22"}}
		}
		var rows []map[string]any
		if r.URL.Query().Get("config") == "text" && r.URL.Query().Get("offset") == "0" {
			rows = []map[string]any{row("Pro C (High)", 1500), row("Cheap B", 1300), row("Promo D", 1200), row("Filler One", 1100), row("Filler Two", 1000)}
		}
		json.NewEncoder(w).Encode(map[string]any{"rows": rows, "num_rows_total": len(rows)})
	}))
	defer hf.Close()
	e.a.BenchBase = hf.URL
	e.a.SWEPage = hf.URL + "/swe"
	e.a.AiderYAML = hf.URL + "/aider"
	if err := e.a.SyncBench(context.Background()); err != nil {
		t.Fatal(err)
	}
	scores, _ := e.a.St.Bench()
	if len(scores) != 3 || scores["pro-c"]["text"].Value <= scores["cheap-b"]["text"].Value ||
		scores["cheap-b"]["text"].Value <= scores["promo-d"]["text"].Value {
		t.Fatalf("scores: %+v", scores)
	}
	rk := e.a.Rt.Rankings()["babu/text"]
	found := map[string]string{}
	for _, c := range rk.Candidates {
		found[c.Model] = c.BenchSource
	}
	if found["pro-c"] != "text" || found["cheap-b"] != "text" {
		t.Fatalf("the ranking must use the leaderboard score: %v", found)
	}
	if e.a.BenchFetched().IsZero() {
		t.Fatal("fetch time not recorded")
	}
	// a restart re-maps the stored rows without a download
	e.a.BenchBase = "http://127.0.0.1:1"
	e.a.Cat.Refresh(context.Background())
	if again, _ := e.a.St.Bench(); len(again) != 3 {
		t.Fatalf("catalog refresh must re-match the stored leaderboard: %d", len(again))
	}
}

// Requirement 7 + the purpose decision: the same catalog and the same preset are scored differently for a
// coding client than for a general one, and a model with no coding score falls back to the general arena
// with the "general score" flag instead of being dropped.
func TestPurposeChangesHowTheTextPresetIsScored(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		// both clients hold the same pool and the same optimization, so only the purpose can differ
		for _, name := range []string{"babu", "coded"} {
			for i := range c.Clients {
				if c.Clients[i].Name == name {
					c.Clients[i].Presets["text"] = config.Preset{Category: "chat", Optimization: "best_quality", Models: []string{"cheap-b", "pro-c", "promo-d"}}
				}
			}
		}
	})
	// the stored leaderboards: pro-c is the best coder, cheap-b the best generalist, promo-d is only
	// rated in general
	if err := e.a.St.ReplaceAllBench(map[string][]store.BenchRow{
		"pro-c":   {{Arena: "swebench", Value: 0.9}, {Arena: "text", Value: 0.30}},
		"cheap-b": {{Arena: "swebench", Value: 0.2}, {Arena: "text", Value: 0.90}},
		"promo-d": {{Arena: "text", Value: 0.40}},
	}); err != nil {
		t.Fatal(err)
	}
	e.a.Rt.Recompute(time.Now())

	rk := e.a.Rt.Rankings()["babu/text"]
	if rk.Purpose != "general" {
		t.Fatalf("babu is a general client: %q", rk.Purpose)
	}
	if len(rk.Candidates) == 0 || rk.Candidates[0].Model != "cheap-b" || rk.Candidates[0].BenchSource != "text" {
		t.Fatalf("a general client must rank on the general arena: %+v", rk.Candidates)
	}
	// the discounted model is served even though it is not the owner's own pick (ADR 0022)
	served := false
	for _, c := range rk.Candidates {
		if c.Model == "promo-d" {
			served = true
		}
	}
	if !served {
		t.Fatalf("a discounted model must be served: %+v", rk.Candidates)
	}

	// the same preset for the coding client
	coded := e.a.Rt.Rankings()["coded/text"]
	if coded == nil || coded.Purpose != "coding" {
		t.Fatalf("the coding client must be ranked: %+v", coded)
	}
	if len(coded.Candidates) == 0 || coded.Candidates[0].Model != "pro-c" || coded.Candidates[0].BenchSource != "swebench" {
		t.Fatalf("a coding client must rank on the coding arenas: %+v", coded.Candidates)
	}
	// a model with no coding score falls back to the general arena and says so
	for _, c := range coded.Candidates {
		if c.Model == "promo-d" && !containsStr(c.Flags, "general score") {
			t.Fatalf("a model with no coding score must be flagged: %+v", c)
		}
	}
	// and the request path serves that model
	r, body := e.post(t, "xl-coded", "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if r.StatusCode != 200 {
		t.Fatalf("chat: %d %s", r.StatusCode, body)
	}
	if got := r.Header.Get("X-Xellter-Model"); got != "pro-c" {
		t.Fatalf("the coding client must be served by the model its purpose ranks first, got %q", got)
	}
}

func containsStr(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}
