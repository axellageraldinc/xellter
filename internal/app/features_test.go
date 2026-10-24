package app_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/config"
)

func twoEqualModels(c *config.Config) {
	sh := c.Shadows["xl-chat"]
	sh.Mode, sh.Models, sh.Strategy = "combo", []string{"cheap-b", "pro-c"}, "round_robin"
	c.Shadows["xl-chat"] = sh
}

func TestRoundRobinRotatesAndSessionsStick(t *testing.T) {
	e := setup(t, twoEqualModels)
	seen := map[string]int{}
	for i := 0; i < 6; i++ { // different prompts = different sessions (identical prompts stick to one model)
		r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(`{"model":"xl-chat","messages":[{"role":"user","content":"question %d"}]}`, i))
		seen[r.Header.Get("X-Xellter-Model")]++
	}
	if seen["cheap-b"] == 0 || seen["pro-c"] == 0 {
		t.Fatalf("round robin must use both models: %v", seen)
	}
	// with a session header, every request of the session lands on the same model
	first := ""
	for i := 0; i < 6; i++ {
		r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-chat"), "X-Xellter-Session", "s1")
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
	rk := e.a.Rt.Rankings()["xl-fast"]
	if len(rk.Trial) != 1 || rk.Trial[0].Model != "promo-d" {
		t.Fatalf("trial pool: %+v rejected: %+v", rk.Trial, rk.Rejected)
	}
	r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-fast"))
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
	r, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "xl-fast"))
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
	scores, _ := e.a.St.JEVScores()
	if scores["free-a:free"].N != 2 {
		t.Fatalf("stored: %+v", scores)
	}
	e.a.Rt.Recompute(time.Now())
	for _, c := range e.a.Rt.Rankings()["xl-chat"].Candidates {
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
	snap := e.a.Cat.Snapshot()
	var plan, topup []string
	for _, k := range e.a.Pool.Keys() {
		st := k.Status(snap, time.Now())
		if st.Name == "plan" {
			plan = st.Learned
		} else {
			topup = st.Learned
		}
	}
	if len(plan) != 2 || len(topup) != 0 {
		t.Fatalf("plan=%v topup=%v", plan, topup)
	}
	for _, c := range e.a.Rt.Rankings()["xl-chat"].Candidates {
		if c.Model == "pro-c" { // topup (unrestricted) can still serve it
			return
		}
	}
	t.Fatal("pro-c should remain routable through the unrestricted key")
}
