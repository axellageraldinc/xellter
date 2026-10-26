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

// snapWithFree limits the free tier to the given numbers, so a test can leave
// the per-minute limit out of the way.
func snapWithFree(rpm, daily int) *catalog.Snapshot {
	s := snap()
	s.Tier.Free = catalog.TierLimit{RPM: rpm, Daily: daily}
	return s
}

func key(t *testing.T, u config.Upstream) *Key {
	t.Helper()
	cfg := config.Default()
	cfg.Clients = []config.Client{{Name: "c", Enabled: true, Keys: []config.Upstream{u}}}
	p := NewPool()
	p.Reload(cfg, map[string]string{u.KeyEnv: "secret"}, nil)
	return p.Keys()[0]
}

// planKey returns a key whose account is known to have the "Indie" plan, as
// xellter sees it after reading the quota from kenari. The weekly window holds
// 1000 Rp.
func planKey(t *testing.T, u config.Upstream) *Key {
	t.Helper()
	k := key(t, u)
	k.Account().SetInfo(&PlanInfo{Name: "Indie", Windows: map[string]Window{
		"week": {BudgetRP: 1000, UsedRP: 0, ResetsAt: now.Add(7 * 24 * time.Hour)},
	}, Fetched: now}, k.Name)
	return k
}

func TestRPMAndDailyFreeLimits(t *testing.T) {
	k := key(t, config.Upstream{Name: "k", KeyEnv: "K"})
	free := &catalog.Model{ID: "f:free", Free: true}
	for i := range 5 {
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
	k2 := key(t, config.Upstream{Name: "k2", KeyEnv: "K"})
	s2 := snapWithFree(1000, 50)
	for i := range 50 {
		if r := k2.Reserve(free, s2, now.Add(time.Duration(i)*time.Second)); r != OK {
			t.Fatalf("daily %d: %s", i, r)
		}
	}
	if r := k2.Reserve(free, s2, now.Add(time.Hour)); r != DailyLimit {
		t.Fatalf("51st free request: %q", r)
	}
	paid := &catalog.Model{ID: "p", InIDR: 1}
	if r := k2.Reserve(paid, s2, now.Add(time.Hour)); r != OK {
		t.Fatalf("daily free quota must not block paid models: %q", r)
	}
	if r := k2.Reserve(free, s2, now.Add(25*time.Hour)); r != OK {
		t.Fatalf("window must roll: %q", r)
	}
}

func TestPlanCoverageAndBudgetExhaustion(t *testing.T) {
	k := planKey(t, config.Upstream{Name: "k", KeyEnv: "K"})
	in := &catalog.Model{ID: "plan-a", InIDR: 1}
	out := &catalog.Model{ID: "other", InIDR: 1}
	if !k.Covered(in, snap(), now) || k.Covered(out, snap(), now) {
		t.Fatal("scope wrong")
	}
	if !k.FreeCache(in, snap(), now) || k.FreeCache(&catalog.Model{ID: "plan-b"}, snap(), now) {
		t.Fatal("free-cache wrong")
	}
	k.RecordSpend(now.Add(time.Minute), 980) // weekly budget is 1000, 97% threshold
	if k.Covered(in, snap(), now.Add(2*time.Minute)) {
		t.Fatal("exhausted weekly budget must uncover plan models")
	}
	if !k.Covered(in, snap(), now.Add(8*24*time.Hour)) {
		t.Fatal("budget must recover when the window has reset")
	}
}

// A key that keeps failing is a key that is down. The numbers are constants now (ADR 0020): there is no
// Resilience knob to pass in.
func TestFailureClassification(t *testing.T) {
	k := planKey(t, config.Upstream{Name: "k", KeyEnv: "K"})
	m := &catalog.Model{ID: "plan-a", InIDR: 1}
	s := snap()

	v := k.Failure("plan-a", 429, []byte(`{"error":{"code":"plan_limit_reached","message":"x"}}`), 0, now)
	if !v.Retry || k.Check(m, s, now.Add(time.Second)) != PlanLimit {
		t.Fatalf("plan limit: %+v / %s", v, k.Check(m, s, now.Add(time.Second)))
	}
	k.ResetState()

	v = k.Failure("plan-a", 429, []byte(`{}`), 20*time.Second, now)
	if !v.Retry || k.Check(m, s, now.Add(5*time.Second)) != Cooldown || k.Check(m, s, now.Add(25*time.Second)) != OK {
		t.Fatal("429 with Retry-After must cool the key for that long")
	}
	k.ResetState()

	if v = k.Failure("plan-a", 400, []byte(`{"error":{"code":"bad_request","message":"messages is required"}}`), 0, now); v.Retry {
		t.Fatal("genuine client errors must not be retried")
	}
	v = k.Failure("plan-a", 400, []byte(`{"error":{"message":"Allowed models: plan-b, free-x:free"}}`), 0, now)
	if !v.Retry || k.Check(m, s, now) != NotAllowed || !k.AllowedModel("plan-b") {
		t.Fatal("allow list not learned from 400")
	}
	k.ResetState()

	// A 5xx is the model's or the provider's problem: it blocks that model and must NOT trip the key's
	// breaker, or one bad model would take every healthy model down with it.
	for range breakerFails {
		k.Failure("plan-a", 503, nil, 0, now)
	}
	if k.Check(&catalog.Model{ID: "other", InIDR: 1}, s, now.Add(time.Second)) != OK {
		t.Fatal("a 5xx on one model must not make the whole key unusable")
	}
	if k.Check(&catalog.Model{ID: "plan-a", InIDR: 1}, s, now.Add(time.Second)) != Blocked {
		t.Fatal("the model that failed must be blocked")
	}
	k.ResetState()
	// A transport failure says nothing about a model, so it does trip the breaker.
	for range breakerFails {
		k.Failure("plan-a", 0, nil, 0, now)
	}
	if k.Check(&catalog.Model{ID: "zzz", InIDR: 1}, s, now.Add(time.Second)) != BreakerOpen {
		t.Fatal("breaker should open after repeated network errors")
	}
	if k.Check(&catalog.Model{ID: "zzz", InIDR: 1}, s, now.Add(breakerOpen+time.Second)) != OK {
		t.Fatal("breaker should close after its open time")
	}
}

// The error codes of kenari's rate-limit page each have their own effect: the free quotas and the balance
// are account-wide, a rejected key is not.
func TestAccountWideFailures(t *testing.T) {
	k := planKey(t, config.Upstream{Name: "k", KeyEnv: "K"})
	s := snap()
	free := &catalog.Model{ID: "f:free", Free: true}
	paid := &catalog.Model{ID: "p", InIDR: 1}

	if v := k.Failure("f:free", 429, []byte(`{"error":{"code":"free_quota_daily"}}`), 0, now); !v.Retry {
		t.Fatal("the free daily quota is retriable")
	}
	if r := k.Check(free, s, now); r != DailyLimit {
		t.Fatalf("free daily quota: %q", r)
	}
	if r := k.Check(paid, s, now); r != OK {
		t.Fatalf("a used free quota must not block paid models: %q", r)
	}
	k.ResetState()

	k.Failure("f:free", 429, []byte(`{"error":{"code":"free_quota_rpm"}}`), 0, now)
	if r := k.Check(free, s, now.Add(time.Second)); r != RPMLimit {
		t.Fatalf("free per-minute quota: %q", r)
	}
	k.ResetState()

	if v := k.Failure("p", 402, []byte(`{"error":{"code":"insufficient_balance"}}`), 0, now); !v.Retry || v.Reason != "insufficient balance" {
		t.Fatalf("402: %+v", v)
	}
	if r := k.Check(paid, s, now); r != NoBalance {
		t.Fatalf("empty balance: %q", r)
	}
	k.ResetState()

	// a provider rate limiting one model blocks that model only
	if v := k.Failure("plan-a", 429, []byte(`{"error":{"code":"upstream_error"}}`), 0, now); !v.Retry {
		t.Fatal("upstream_error is retriable")
	}
	if r := k.Check(&catalog.Model{ID: "plan-a"}, s, now); r != Blocked {
		t.Fatalf("the model must be blocked: %q", r)
	}
	if r := k.Check(&catalog.Model{ID: "plan-b"}, s, now); r != OK {
		t.Fatalf("another model must stay usable: %q", r)
	}
	k.ResetState()

	// a rejected key cools down on its own
	if v := k.Failure("p", 401, nil, 0, now); !v.Retry {
		t.Fatal("401 is retriable on another key")
	}
	if r := k.Check(paid, s, now.Add(time.Second)); r != Cooldown {
		t.Fatalf("rejected key: %q", r)
	}
	if r := k.Check(paid, s, now.Add(6*time.Minute)); r != OK {
		t.Fatalf("the cooldown must end: %q", r)
	}
}

func TestBlockBackoffGrowsAndSuccessResets(t *testing.T) {
	k := key(t, config.Upstream{Name: "k", KeyEnv: "K"})
	m := &catalog.Model{ID: "x", InIDR: 1}
	k.Failure("x", 500, nil, 0, now)
	if k.Check(m, snap(), now.Add(10*time.Second)) != Blocked || k.Check(m, snap(), now.Add(cooldownBase+time.Second)) != OK {
		t.Fatal("first block should last the base cooldown")
	}
	k.Failure("x", 500, nil, 0, now.Add(time.Minute))
	if k.Check(m, snap(), now.Add(time.Minute+45*time.Second)) != Blocked {
		t.Fatal("second block should be longer (exponential)")
	}
	k.Success("x", now.Add(10*time.Minute))
	if k.Check(m, snap(), now.Add(10*time.Minute)) != OK {
		t.Fatal("success must clear the block")
	}
}

func TestSeededFromStoreAndLearnedAllowList(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.Log(store.Request{TS: time.Now().UnixMilli(), Upstream: "c/k", Model: "f:free", OK: true, Billing: "free"})
	st.Flush()
	cfg := config.Default()
	cfg.Clients = []config.Client{{Name: "c", Enabled: true, Keys: []config.Upstream{{Name: "k", KeyEnv: "K"}}}}
	p := NewPool()
	p.Reload(cfg, map[string]string{"K": "s"}, st)
	k := p.Keys()[0]
	if k.Account().Status(snap(), time.Now()).DailyUsed != 1 {
		t.Fatal("counters must be rebuilt from the request log after a restart")
	}
	// the allow list comes from kenari (a probe or a 400 answer), never from the config
	k.SetAllowed([]string{"deepseek-v4"})
	if k.AllowedModel("glm-5") || !k.AllowedModel("deepseek-v4") {
		t.Fatal("learned allow list ignored")
	}
	k.SetAllowed(nil)
	if !k.AllowedModel("glm-5") {
		t.Fatal("an unrestricted key may call every model")
	}
}

// The pool keeps the state of keys whose name did not change, so a config reload does not forget a cooldown,
// and the read-only account key is marked as such: it reads plan and balance, it never serves traffic.
func TestReloadKeepsStateAndMarksTheAccountKey(t *testing.T) {
	cfg := config.Default()
	cfg.Clients = []config.Client{{Name: "c", Enabled: true, Keys: []config.Upstream{{Name: "k", KeyEnv: "K"}}}}
	p := NewPool()
	p.Reload(cfg, map[string]string{"K": "s", "KENARI_ACCOUNT_KEY": "a"}, nil)
	k := p.Get("c/k")
	k.Failure("m", 500, nil, 0, now)
	p.Reload(cfg, map[string]string{"K": "s", "KENARI_ACCOUNT_KEY": "a"}, nil)
	if again := p.Get("c/k"); again != k || again.Check(&catalog.Model{ID: "m"}, snap(), now.Add(time.Second)) != Blocked {
		t.Fatal("a reload must keep the key's failure state")
	}
	if p.Get("c/k").ReadOnly() {
		t.Fatal("a client key is not read-only")
	}
	acct := p.Get("account")
	if acct == nil || !acct.ReadOnly() || acct.client != "" {
		t.Fatalf("the account key: %+v", acct)
	}
	if keys := p.ClientKeys("c"); len(keys) != 1 {
		t.Fatalf("client keys: %v", keys)
	}
	if keys := p.ClientKeys(""); len(keys) != 0 {
		t.Fatal("the empty client name has no keys")
	}
	if len(p.Accounts()) != 1 || p.Accounts()[0].Name != DefaultAccount {
		t.Fatalf("there is exactly one kenari account: %v", p.Accounts())
	}
}

func TestParseAllowed(t *testing.T) {
	list, ok := ParseAllowed([]byte(`{"error":{"message":"model 'x' is not allowed by this key. Allowed models: cheap-b, free-a:free"}}`))
	if !ok || len(list) != 2 || list[0] != "cheap-b" || list[1] != "free-a:free" {
		t.Fatalf("allow list: %v %v", list, ok)
	}
	if _, ok := ParseAllowed([]byte(`{"error":{"message":"bad request"}}`)); ok {
		t.Fatal("an error without a list must not be parsed as one")
	}
}
