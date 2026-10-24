package upstream

import (
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func snap() *catalog.Snapshot {
	return &catalog.Snapshot{
		Models: map[string]*catalog.Model{},
		Tier:   catalog.FreeTier{Free: catalog.TierLimit{RPM: 5, Daily: 50}, Topup: catalog.TierLimit{RPM: 10, Daily: 1000}, Plan: catalog.TierLimit{RPM: 25}},
		Plans:  []catalog.Plan{{Name: "Indie", BudgetWeek: 1000, BudgetMon: 4000, Scope: []string{"plan-*"}, FreeCache: []string{"plan-a"}}},
	}
}

func key(t *testing.T, u config.Upstream) *Key {
	cfg := config.Default()
	u.Enabled = true
	cfg.Upstreams = []config.Upstream{u}
	p := NewPool()
	p.Reload(cfg, map[string]string{u.KeyEnv: "secret"}, nil)
	return p.Keys()[0]
}

var res = config.Resilience{CooldownBaseSec: 30, CooldownMaxSec: 900, BreakerFailures: 3, BreakerOpenSec: 120}

func TestRPMAndDailyFreeLimits(t *testing.T) {
	k := key(t, config.Upstream{Name: "k", KeyEnv: "K", Tier: "free"})
	free := &catalog.Model{ID: "f:free", Free: true}
	for i := 0; i < 5; i++ {
		if r := k.Reserve(free, snap(), now); r != OK {
			t.Fatalf("request %d: %s", i, r)
		}
	}
	if r := k.Reserve(free, snap(), now); r != RPMLimit {
		t.Fatalf("6th request in a minute: %q", r)
	}
	if r := k.Reserve(free, snap(), now.Add(61*time.Second)); r != OK {
		t.Fatalf("after the minute: %q", r)
	}
	// daily: 50 free requests per rolling day on the free tier
	k2 := key(t, config.Upstream{Name: "k2", KeyEnv: "K", Tier: "free", RPM: 1000})
	for i := 0; i < 50; i++ {
		if r := k2.Reserve(free, snap(), now.Add(time.Duration(i)*time.Second)); r != OK {
			t.Fatalf("daily %d: %s", i, r)
		}
	}
	if r := k2.Reserve(free, snap(), now.Add(time.Hour)); r != DailyLimit {
		t.Fatalf("51st free request: %q", r)
	}
	paid := &catalog.Model{ID: "p", InIDR: 1}
	if r := k2.Reserve(paid, snap(), now.Add(time.Hour)); r != OK {
		t.Fatalf("daily free quota must not block paid models: %q", r)
	}
	if r := k2.Reserve(free, snap(), now.Add(25*time.Hour)); r != OK {
		t.Fatalf("window must roll: %q", r)
	}
}

func TestPlanCoverageAndBudgetExhaustion(t *testing.T) {
	k := key(t, config.Upstream{Name: "k", KeyEnv: "K", Tier: "plan", Plan: "Indie"})
	in := &catalog.Model{ID: "plan-a", InIDR: 1}
	out := &catalog.Model{ID: "other", InIDR: 1}
	if !k.Covered(in, snap(), now) || k.Covered(out, snap(), now) {
		t.Fatal("scope wrong")
	}
	if !k.FreeCache(in, snap(), now) || k.FreeCache(&catalog.Model{ID: "plan-b"}, snap(), now) {
		t.Fatal("free-cache wrong")
	}
	k.RecordSpend(now.Add(-time.Hour), 980) // weekly budget is 1000, 97% threshold
	if k.Covered(in, snap(), now) {
		t.Fatal("exhausted weekly budget must uncover plan models")
	}
	if !k.Covered(in, snap(), now.Add(8*24*time.Hour)) {
		t.Fatal("budget must recover when old spend leaves the window")
	}
}

func TestFailureClassification(t *testing.T) {
	k := key(t, config.Upstream{Name: "k", KeyEnv: "K", Tier: "plan", Plan: "Indie"})
	m := &catalog.Model{ID: "plan-a", InIDR: 1}
	s := snap()

	v := k.Failure("plan-a", 429, []byte(`{"error":{"code":"plan_limit_reached","message":"x"}}`), 0, res, now)
	if !v.Retry || k.Check(m, s, now.Add(time.Second)) != PlanLimit {
		t.Fatalf("plan limit: %+v / %s", v, k.Check(m, s, now.Add(time.Second)))
	}
	k.ResetState()

	v = k.Failure("plan-a", 429, []byte(`{}`), 20*time.Second, res, now)
	if !v.Retry || k.Check(m, s, now.Add(5*time.Second)) != Cooldown || k.Check(m, s, now.Add(25*time.Second)) != OK {
		t.Fatal("429 with Retry-After must cool the key for that long")
	}
	k.ResetState()

	if v = k.Failure("plan-a", 400, []byte(`{"error":{"code":"bad_request","message":"messages is required"}}`), 0, res, now); v.Retry {
		t.Fatal("genuine client errors must not be retried")
	}
	v = k.Failure("plan-a", 400, []byte(`{"error":{"message":"Allowed models: plan-b, free-x:free"}}`), 0, res, now)
	if !v.Retry || k.Check(m, s, now) != NotAllowed || k.AllowedModel("plan-b") != true {
		t.Fatal("allow list not learned from 400")
	}
	k.ResetState()

	for i := 0; i < 3; i++ {
		k.Failure("plan-a", 503, nil, 0, res, now)
	}
	if k.Check(&catalog.Model{ID: "zzz", InIDR: 1}, s, now.Add(time.Second)) != BreakerOpen {
		t.Fatal("breaker should open after repeated 5xx")
	}
	if k.Check(&catalog.Model{ID: "zzz", InIDR: 1}, s, now.Add(3*time.Minute)) != OK {
		t.Fatal("breaker should close after its open time")
	}
}

func TestBlockBackoffGrowsAndSuccessResets(t *testing.T) {
	k := key(t, config.Upstream{Name: "k", KeyEnv: "K", Tier: "free"})
	m := &catalog.Model{ID: "x", InIDR: 1}
	k.Failure("x", 500, nil, 0, res, now)
	if k.Check(m, snap(), now.Add(10*time.Second)) != Blocked || k.Check(m, snap(), now.Add(31*time.Second)) != OK {
		t.Fatal("first block should last the base cooldown")
	}
	k.Failure("x", 500, nil, 0, res, now.Add(time.Minute))
	if k.Check(m, snap(), now.Add(time.Minute+45*time.Second)) != Blocked {
		t.Fatal("second block should be longer (exponential)")
	}
	k.Success("x", now.Add(10*time.Minute))
	if k.Check(m, snap(), now.Add(10*time.Minute)) != OK {
		t.Fatal("success must clear the block")
	}
}

func TestSeededFromStoreAndConfigAllowList(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.Log(store.Request{TS: time.Now().UnixMilli(), Upstream: "k", Model: "f:free", OK: true, Billing: "free"})
	st.Flush()
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{{Name: "k", KeyEnv: "K", Enabled: true, Tier: "free", Allow: []string{"deepseek-*"}}}
	p := NewPool()
	p.Reload(cfg, map[string]string{"K": "s"}, st)
	k := p.Keys()[0]
	if k.Status(snap(), time.Now()).DailyUsed != 1 {
		t.Fatal("counters must be rebuilt from the request log after a restart")
	}
	if k.AllowedModel("glm-5") || !k.AllowedModel("deepseek-v4") {
		t.Fatal("config allow globs ignored")
	}
}
