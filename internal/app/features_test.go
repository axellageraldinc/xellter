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
	"github.com/axellageraldinc/xellter/internal/rank"
)

func twoEqualModels(c *config.Config) {
	sh := c.Clients[0].Presets["text"]
	sh.Mode, sh.Models, sh.Strategy = "combo", []string{"cheap-b", "pro-c"}, "round_robin"
	c.Clients[0].Presets["text"] = sh
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

func TestCanarySendsTrafficToNewModelAndFallsBack(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.Rules.CanaryPercent = 100 })
	// mark one model as brand new after the baseline
	e.a.St.SetSeen("promo-d", time.Now().Unix())
	e.a.Rt.Recompute(time.Now())
	rk := e.a.Rt.Rankings()["babu/budget"]
	if len(rk.Trial) != 1 || rk.Trial[0].Model != "promo-d" {
		t.Fatalf("trial pool: %+v rejected: %+v", rk.Trial, rk.Rejected)
	}
	r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "budget"))
	if r.Header.Get("X-Xellter-Model") != "promo-d" {
		t.Fatalf("canary should serve first, got %s", r.Header.Get("X-Xellter-Model"))
	}
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "promo-d" {
			return 503, `{"error":{"message":"boom"}}`
		}
		return 0, ""
	}
	e.a.Pool.Keys()[0].ResetState()
	e.a.Pool.Keys()[1].ResetState()
	r, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "budget"))
	if r.StatusCode != 200 || r.Header.Get("X-Xellter-Model") == "promo-d" {
		t.Fatalf("a failing canary must fall back to a proven model: %d %s %s", r.StatusCode, r.Header.Get("X-Xellter-Model"), body)
	}
}

func TestJEVEvaluationStoresScoreAndRespectsBudget(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.JEV.Enabled = true
		c.JEV.Prompts = []string{"say hi", "say bye"}
		c.JEV.DailyBudgetIDR = 5
	})
	e.a.Judge.HC = e.f.srv.Client()
	score, err := e.a.Judge.Evaluate(context.Background(), "free-a:free")
	if err != nil || score < 0.89 || score > 0.91 {
		t.Fatalf("score=%v err=%v", score, err)
	}
	for _, c := range e.f.calls {
		if (c.Path == "/v1/systemone" || c.Model == "free-a:free") && c.Auth != "Bearer secret-account" {
			t.Fatalf("a call that belongs to no client must use the account key, used %q on %s", c.Auth, c.Path)
		}
	}
	scores, _ := e.a.St.JEVScores()
	if scores["free-a:free"].N != 2 {
		t.Fatalf("stored: %+v", scores)
	}
	e.a.Rt.Recompute(time.Now())
	for _, c := range e.a.Rt.Rankings()["babu/text"].Candidates {
		if c.Model == "free-a:free" && c.Components["jev"] < 0.89 {
			t.Fatalf("JEV score must feed the ranking: %+v", c.Components)
		}
	}
	e.a.Judge.Cfg().JEV.DailyBudgetIDR = 0
	if _, err := e.a.Judge.RunDue(context.Background(), 3); err == nil {
		t.Fatal("a zero budget must stop evaluation")
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

func TestAccountQuotaFromUnrestrictedKeyReplacesGuess(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		for i := range c.Clients[0].Keys {
			c.Clients[0].Keys[i].Account = "main"
			c.Clients[0].Keys[i].Plan = "WrongGuess"
		}
		c.Clients[0].Keys[0].Tier, c.Clients[0].Keys[1].Tier = "plan", "plan"
	})
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
	e.a.Pool.Keys()[0].Failure("cheap-b", 429, []byte(`{"error":{"code":"rate_limit_exceeded"}}`), 0, e.a.Cfg().Resilience, time.Now())
	if r := e.a.Pool.Keys()[1].Check(m, e.a.Cat.Snapshot(), time.Now()); r != "cooldown" {
		t.Fatalf("rate limits are per account, got %q", r)
	}
}

func TestFreeQuotaErrorsAreAccountWide(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Keys[0].Account, c.Clients[0].Keys[1].Account = "main", "main"
	})
	free := e.a.Cat.Snapshot().Models["free-a:free"]
	paid := e.a.Cat.Snapshot().Models["pro-c"]
	k0, k1 := e.a.Pool.Keys()[0], e.a.Pool.Keys()[1]
	now := time.Now()
	k0.Failure("free-a:free", 429, []byte(`{"error":{"code":"free_quota_daily","message":"used"}}`), 0, e.a.Cfg().Resilience, now)
	if r := k1.Check(free, e.a.Cat.Snapshot(), now); r != "daily free quota used" {
		t.Fatalf("free daily quota is per account, got %q", r)
	}
	if r := k1.Check(paid, e.a.Cat.Snapshot(), now); r != "" {
		t.Fatalf("paid models must stay usable: %q", r)
	}
}

func TestPresetsAreGeneratedFromWhatTheKeysCanCall(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients = append(c.Clients, config.Client{Name: "gen", Enabled: true, KeyHash: proxy.HashKey("xl-gen"),
			Keys: []config.Upstream{{Name: "limited", KeyEnv: "KEY_GEN", Enabled: true, Tier: "topup"}}})
	})
	if err := config.SetSecret(e.a.Cfg().Server.SecretsFile, "KEY_GEN", "secret-gen"); err != nil {
		t.Fatal(err)
	}
	e.a.Pool.Reload(e.a.Cfg(), e.a.Secrets(), e.a.St)
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "xellter-probe" && c.Auth == "Bearer secret-gen" {
			return 400, `{"error":{"code":"bad_request","message":"model 'xellter-probe' is not allowed by this key. Allowed models: cheap-b, free-a:free, img-free:free, jev-1-13:free"}}`
		}
		return 0, ""
	}
	e.a.ProbeKeys(context.Background())
	gen := func() config.Client {
		for _, c := range e.a.Cfg().Clients {
			if c.Name == "gen" {
				return c
			}
		}
		return config.Client{}
	}
	got := gen()
	if _, ok := got.Presets["text"]; !ok || got.Presets["image"].Modality != "images" || len(got.Presets) != 2 {
		t.Fatalf("expected exactly text and image, got %v", got.Presets)
	}
	if rk := e.a.Rt.Rankings()["gen/text"]; rk == nil || rk.Callable != 2 { // cheap-b and free-a:free: deduplicated, limited to the key's models and the preset's needs
		t.Fatalf("text ranking: %+v", rk)
	}
	if rk := e.a.Rt.Rankings()["gen/video"]; rk != nil {
		t.Fatal("no video preset without video models")
	}
	// a preset the owner deleted is not generated again, and edits survive later probes
	next := *e.a.Cfg()
	next.Clients = append([]config.Client(nil), next.Clients...)
	for i := range next.Clients {
		if next.Clients[i].Name == "gen" {
			cl := next.Clients[i]
			cl.Presets = map[string]config.Preset{"image": cl.Presets["image"]}
			p := cl.Presets["image"]
			p.Strategy = "round_robin"
			cl.Presets["image"] = p
			next.Clients[i] = cl
		}
	}
	if err := e.a.Apply(&next); err != nil {
		t.Fatal(err)
	}
	e.a.ProbeKeys(context.Background())
	got = gen()
	if _, ok := got.Presets["text"]; ok {
		t.Fatal("a deleted preset came back")
	}
	if got.Presets["image"].Strategy != "round_robin" {
		t.Fatal("an edited preset was overwritten")
	}
}

func TestJEVStopsTheRoundWhenTheJudgeIsDown(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.JEV.Enabled = true; c.JEV.Prompts = []string{"say hi"} })
	e.a.Judge.HC = e.f.srv.Client()
	e.f.judge = func(string) (int, string) {
		return 503, `{"error":{"code":"all_providers_failed","message":"upstream unavailable"}}`
	}
	n, err := e.a.Judge.RunDue(context.Background(), 5)
	if err == nil || n != 0 || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("n=%d err=%v", n, err)
	}
	calls := e.f.count("cheap-b") + e.f.count("free-a:free") + e.f.count("pro-c")
	if calls > 1 {
		t.Fatalf("a failing judge must not make us generate answers for every model: %d candidate calls", calls)
	}
	if last := e.a.Judge.Last(); last.Error == "" || last.Judged != 0 {
		t.Fatalf("panel status: %+v", last)
	}
	// while the judge is known to be down, nothing is spent on candidate answers
	before := e.f.count("cheap-b") + e.f.count("free-a:free") + e.f.count("pro-c")
	if _, err := e.a.Judge.Evaluate(context.Background(), "cheap-b"); err == nil {
		t.Fatal("expected the cool-down error")
	}
	if after := e.f.count("cheap-b") + e.f.count("free-a:free") + e.f.count("pro-c"); after != before {
		t.Fatal("candidate answers were requested although the judge is down")
	}
}

func TestClientsNeverSeeEachOthersModels(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		text := config.DefaultPreset("chat")
		text.MinQuality, text.Require = 0, config.Require{}
		for _, n := range []string{"one", "two"} {
			c.Clients = append(c.Clients, config.Client{Name: n, Enabled: true, KeyHash: proxy.HashKey("xl-" + n), Presets: map[string]config.Preset{"text": text}, Seeded: config.PresetModalities,
				Keys: []config.Upstream{{Name: "k", KeyEnv: "KEY_" + strings.ToUpper(n), Enabled: true, Tier: "topup", Allow: map[string][]string{"one": {"cheap-b"}, "two": {"pro-c", "promo-d"}}[n]}}})
		}
	})
	for _, n := range []string{"one", "two"} {
		if err := config.SetSecret(e.a.Cfg().Server.SecretsFile, "KEY_"+strings.ToUpper(n), "secret-"+n); err != nil {
			t.Fatal(err)
		}
	}
	e.a.Pool.Reload(e.a.Cfg(), e.a.Secrets(), e.a.St)
	e.a.Rt.Recompute(time.Now())
	models := func(client string) []string {
		rk := e.a.Rt.Rankings()[client+"/text"]
		if rk == nil {
			t.Fatalf("no ranking for %s", client)
		}
		return rk.Models
	}
	if got := models("one"); len(got) != 1 || got[0] != "cheap-b" {
		t.Fatalf("client one may only see cheap-b, got %v", got)
	}
	if got := models("two"); len(got) != 2 {
		t.Fatalf("client two may only see pro-c and promo-d, got %v", got)
	}
	// and the request path agrees: client one cannot reach a model of client two
	r, _ := e.post(t, "xl-one", "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}]}`)
	if r.Header.Get("X-Xellter-Model") != "cheap-b" {
		t.Fatalf("served by %q", r.Header.Get("X-Xellter-Model"))
	}
}

func TestJEVNeedsTheAccountKey(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.JEV.Enabled = true; c.Kenari.AccountKeyEnv = "KENARI_NOT_SET" })
	e.a.Judge.HC = e.f.srv.Client()
	if _, err := e.a.Judge.Evaluate(context.Background(), "cheap-b"); err == nil || !strings.Contains(err.Error(), "account key") {
		t.Fatalf("judging without an account key must say so, got %v", err)
	}
	if len(e.f.calls) != 0 {
		t.Fatal("no call may be made with a client's key")
	}
}

func TestJudgeFallsBackToTheNextSystemOneModel(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.JEV.Enabled = true; c.JEV.Prompts = []string{"say hi"} })
	e.a.Judge.HC = e.f.srv.Client()
	e.f.judge = func(model string) (int, string) {
		if model == "jev-1-13:free" {
			return 503, `{"error":{"code":"all_providers_failed","message":"upstream unavailable"}}`
		}
		return 200, `{"model":"` + model + `","answers":{"q":{"type":"noul","noul":0.8}},"usage":{"input_tokens":50,"output_tokens":0}}`
	}
	score, err := e.a.Judge.Evaluate(context.Background(), "free-a:free")
	if err != nil || score < 0.79 || score > 0.81 {
		t.Fatalf("clef:free should have judged when jev-1-13:free is down: score=%v err=%v", score, err)
	}
	var tried []string
	for _, c := range e.f.calls {
		if c.Path == "/v1/systemone" {
			tried = append(tried, c.Model)
		}
	}
	if len(tried) != 2 || tried[0] != "jev-1-13:free" || tried[1] != "clef:free" {
		t.Fatalf("judges tried in order: %v", tried)
	}
}

func TestJudgeOrderIsTheConfiguredOrder(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.JEV.Enabled = true
		c.JEV.Prompts = []string{"say hi"}
		c.JEV.Judges = []string{"clef:free", "jev-1-13:free"} // the owner puts clef first
	})
	e.a.Judge.HC = e.f.srv.Client()
	e.f.judge = func(model string) (int, string) {
		if model == "clef:free" {
			return 503, `{"error":{"code":"all_providers_failed","message":"upstream unavailable"}}`
		}
		return 200, `{"model":"` + model + `","answers":{"q":{"type":"noul","noul":0.7}},"usage":{"input_tokens":50,"output_tokens":0}}`
	}
	chain := e.a.Judge.Chain()
	if len(chain) < 2 || chain[0].ID != "clef:free" || !chain[0].Configured || chain[1].ID != "jev-1-13:free" {
		t.Fatalf("chain: %+v", chain)
	}
	score, err := e.a.Judge.Evaluate(context.Background(), "free-a:free")
	if err != nil || score < 0.69 || score > 0.71 {
		t.Fatalf("score=%v err=%v", score, err)
	}
	var tried []string
	for _, c := range e.f.calls {
		if c.Path == "/v1/systemone" {
			tried = append(tried, c.Model)
		}
	}
	if len(tried) != 2 || tried[0] != "clef:free" || tried[1] != "jev-1-13:free" {
		t.Fatalf("judges must be tried in the configured order: %v", tried)
	}
}

func TestLeaderboardScoresReplaceThePriceGuess(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.Benchmarks = map[string]float64{} })
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
	if err := e.a.SyncBench(context.Background()); err != nil {
		t.Fatal(err)
	}
	scores, _ := e.a.St.Bench()
	if len(scores) != 3 || scores["pro-c"].Value <= scores["cheap-b"].Value || scores["cheap-b"].Value <= scores["promo-d"].Value {
		t.Fatalf("scores: %+v", scores)
	}
	rk := e.a.Rt.Rankings()["babu/text"]
	found := map[string]string{}
	for _, c := range append(append([]rank.Candidate{}, rk.Candidates...), rk.Trial...) {
		found[c.Model] = c.BenchSource
	}
	if !strings.HasPrefix(found["pro-c"], "arena 2026-10-22") || !strings.HasPrefix(found["cheap-b"], "arena") {
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
