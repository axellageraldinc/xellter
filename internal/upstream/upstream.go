// Package upstream models kenari API keys: rate limits, free-tier quota,
// plan budgets, cooldowns and a circuit breaker. Everything here is in memory
// and seeded from the request log at startup.
package upstream

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
)

// Ledger tracks plan-billed spend. Keys of one kenari account share a ledger
// because a plan's budget belongs to the account, not to a single key.
type Ledger struct {
	mu    sync.Mutex
	spend []store.TimedCost
}

// Add records spend and drops entries older than the longest plan window.
func (l *Ledger) Add(ts time.Time, cost float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.spend = append(l.spend, store.TimedCost{TS: ts.UnixMilli(), Cost: cost})
	cut := ts.Add(-31 * 24 * time.Hour).UnixMilli()
	i := 0
	for i < len(l.spend) && l.spend[i].TS < cut {
		i++
	}
	l.spend = l.spend[i:]
}

// Sums returns spend in the last 5 hours, 7 days and 30 days.
func (l *Ledger) Sums(now time.Time) (s5, sw, sm float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.spend {
		age := now.Sub(time.UnixMilli(s.TS))
		if age <= 5*time.Hour {
			s5 += s.Cost
		}
		if age <= 7*24*time.Hour {
			sw += s.Cost
		}
		if age <= 30*24*time.Hour {
			sm += s.Cost
		}
	}
	return
}

// Key is one upstream API key with its live limiter state.
type Key struct {
	Name   string
	Secret string

	mu           sync.Mutex
	cfg          config.Upstream
	rpm          []int64 // request times (ms), last minute
	free         []int64 // free-model request times (ms), last 24h
	led          *Ledger
	cooldown     time.Time // key unusable until
	planBlocked  time.Time // plan-covered models unusable until (plan_limit_reached)
	balanceEmpty time.Time // paid models unusable until (insufficient_balance)
	fails        int       // consecutive failures
	breaker      time.Time
	blocked      map[string]time.Time // model -> until
	blockN       map[string]int
	learned      map[string]struct{} // models this key may use (learned from 400s); nil = unknown
	lastErr      string
	lastOK       time.Time
}

// Pool is the set of keys.
type Pool struct {
	mu      sync.RWMutex
	keys    []*Key
	by      map[string]*Key
	ledgers map[string]*Ledger
	rr      atomic.Uint64
}

// NewPool returns an empty pool.
func NewPool() *Pool { return &Pool{by: map[string]*Key{}, ledgers: map[string]*Ledger{}} }

// Reload applies a new config; state of keys with unchanged names is kept.
// st may be nil (tests); it seeds counters for brand-new keys.
func (p *Pool) Reload(cfg *config.Config, secrets map[string]string, st *store.Store) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := make([]*Key, 0, len(cfg.Upstreams))
	by := map[string]*Key{}
	ledgers := map[string]*Ledger{}
	for _, u := range cfg.Upstreams {
		secret := secrets[u.KeyEnv]
		k := p.by[u.Name]
		if k == nil {
			k = &Key{Name: u.Name, blocked: map[string]time.Time{}, blockN: map[string]int{}}
			if st != nil {
				now := time.Now()
				if t, err := st.FreeTimes(u.Name, now.Add(-24*time.Hour)); err == nil {
					k.free = t
				}
				if t, err := st.AllTimes(u.Name, now.Add(-time.Minute)); err == nil {
					k.rpm = t
				}
			}
		}
		acct := u.Account
		if acct == "" {
			acct = u.Name
		}
		led := ledgers[acct]
		if led == nil {
			led = p.ledgers[acct]
			if led == nil {
				led = &Ledger{}
			}
			ledgers[acct] = led
		}
		if _, existing := p.by[u.Name]; !existing && st != nil {
			if sp, err := st.PlanSpend(u.Name, time.Now().Add(-30*24*time.Hour)); err == nil {
				for _, x := range sp {
					led.Add(time.UnixMilli(x.TS), x.Cost)
				}
			}
		}
		k.mu.Lock()
		k.cfg, k.Secret, k.led = u, secret, led
		k.mu.Unlock()
		next = append(next, k)
		by[u.Name] = k
	}
	p.keys, p.by, p.ledgers = next, by, ledgers
}

// Keys returns the current keys.
func (p *Pool) Keys() []*Key {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]*Key(nil), p.keys...)
}

// Get returns a key by name.
func (p *Pool) Get(name string) *Key {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.by[name]
}

// Next returns a monotonically increasing counter for round-robin rotation.
func (p *Pool) Next() uint64 { return p.rr.Add(1) }

// Config returns the key's config.
func (k *Key) Config() config.Upstream {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cfg
}

// Limits returns the effective (rpm, dailyFree) for the key; 0 = unlimited.
func (k *Key) Limits(t catalog.FreeTier) (rpm, daily int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.limits(t)
}

func (k *Key) limits(t catalog.FreeTier) (rpm, daily int) {
	var tl catalog.TierLimit
	switch k.cfg.Tier {
	case "plan":
		tl = t.Plan
	case "topup":
		tl = t.Topup
	default:
		tl = t.Free
	}
	rpm, daily = tl.RPM, tl.Daily
	if k.cfg.RPM > 0 {
		rpm = k.cfg.RPM
	}
	switch {
	case k.cfg.Daily > 0:
		daily = k.cfg.Daily
	case k.cfg.Daily < 0:
		daily = 0
	}
	return
}

func prune(ts []int64, cut int64) []int64 {
	i := 0
	for i < len(ts) && ts[i] < cut {
		i++
	}
	return ts[i:]
}

// AllowedModel reports whether config / learned lists permit the model.
func (k *Key) AllowedModel(id string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.allowed(id)
}

func (k *Key) allowed(id string) bool {
	if len(k.cfg.Allow) > 0 && !config.MatchAny(k.cfg.Allow, id) {
		return false
	}
	if k.learned != nil {
		_, ok := k.learned[id]
		return ok
	}
	return true
}

// Covered reports whether the model is billed to the key's plan right now.
func (k *Key) Covered(m *catalog.Model, snap *catalog.Snapshot, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.covered(m, snap, now)
}

func (k *Key) covered(m *catalog.Model, snap *catalog.Snapshot, now time.Time) bool {
	if k.cfg.Tier != "plan" || now.Before(k.planBlocked) {
		return false
	}
	pl := snap.Plan(k.cfg.Plan)
	if pl == nil || !config.MatchAny(pl.Scope, m.ID) {
		return false
	}
	s5, sw, sm := k.led.Sums(now)
	over := func(spent, budget float64) bool { return budget > 0 && spent >= budget*0.97 }
	return !(over(s5, pl.Budget5h) || over(sw, pl.BudgetWeek) || over(sm, pl.BudgetMon))
}

// FreeCache reports whether cache tokens of the model are free on this key's plan.
func (k *Key) FreeCache(m *catalog.Model, snap *catalog.Snapshot, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.covered(m, snap, now) {
		return false
	}
	pl := snap.Plan(k.cfg.Plan)
	return pl != nil && config.MatchAny(pl.FreeCache, m.ID)
}

// PlanUsage reports consumed/budget per window for the dashboard.
func (k *Key) PlanUsage(snap *catalog.Snapshot, now time.Time) (spent [3]float64, budget [3]float64, plan string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	pl := snap.Plan(k.cfg.Plan)
	if k.cfg.Tier != "plan" || pl == nil {
		return
	}
	spent[0], spent[1], spent[2] = k.led.Sums(now)
	return spent, [3]float64{pl.Budget5h, pl.BudgetWeek, pl.BudgetMon}, pl.Name
}

// Reason codes returned by Check.
const (
	OK          = ""
	Disabled    = "disabled"
	NoSecret    = "no secret"
	Cooldown    = "cooldown"
	BreakerOpen = "breaker open"
	NotAllowed  = "model not allowed on key"
	Blocked     = "model blocked (recent errors)"
	PlanLimit   = "plan limit reached"
	NoBalance   = "no balance"
	RPMLimit    = "rpm limit"
	DailyLimit  = "daily free quota used"
)

// Check reports why the key cannot take a request for the model right now.
// It does not reserve anything.
func (k *Key) Check(m *catalog.Model, snap *catalog.Snapshot, now time.Time) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.check(m, snap, now)
}

func (k *Key) check(m *catalog.Model, snap *catalog.Snapshot, now time.Time) string {
	switch {
	case !k.cfg.Enabled:
		return Disabled
	case k.Secret == "":
		return NoSecret
	case now.Before(k.cooldown):
		return Cooldown
	case now.Before(k.breaker):
		return BreakerOpen
	case !k.allowed(m.ID):
		return NotAllowed
	}
	if until, ok := k.blocked[m.ID]; ok && now.Before(until) {
		return Blocked
	}
	inPlan := k.cfg.Tier == "plan" && k.planScope(m, snap)
	if inPlan && now.Before(k.planBlocked) && !m.Free {
		return PlanLimit
	}
	if !m.Free && !inPlan && now.Before(k.balanceEmpty) {
		return NoBalance
	}
	rpm, daily := k.limits(snap.Tier)
	if rpm > 0 {
		k.rpm = prune(k.rpm, now.Add(-time.Minute).UnixMilli())
		if len(k.rpm) >= rpm {
			return RPMLimit
		}
	}
	if m.Free && daily > 0 {
		k.free = prune(k.free, now.Add(-24*time.Hour).UnixMilli())
		if len(k.free) >= daily {
			return DailyLimit
		}
	}
	return OK
}

func (k *Key) planScope(m *catalog.Model, snap *catalog.Snapshot) bool {
	pl := snap.Plan(k.cfg.Plan)
	return pl != nil && config.MatchAny(pl.Scope, m.ID)
}

// Reserve checks and, if usable, counts the request against the limits.
func (k *Key) Reserve(m *catalog.Model, snap *catalog.Snapshot, now time.Time) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if r := k.check(m, snap, now); r != OK {
		return r
	}
	k.rpm = append(k.rpm, now.UnixMilli())
	if m.Free {
		k.free = append(k.free, now.UnixMilli())
	}
	return OK
}

// RecordSpend adds plan-billed catalog cost to the account ledger.
func (k *Key) RecordSpend(ts time.Time, cost float64) {
	k.mu.Lock()
	led := k.led
	k.mu.Unlock()
	led.Add(ts, cost)
}

// SetAllowed replaces the learned allow list (nil = unrestricted).
func (k *Key) SetAllowed(models []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if models == nil {
		k.learned = nil
		return
	}
	k.learned = make(map[string]struct{}, len(models))
	for _, m := range models {
		k.learned[m] = struct{}{}
	}
}

// ParseAllowed extracts the model list from a kenari "not allowed by this key" error body.
func ParseAllowed(body []byte) ([]string, bool) {
	_, msg := errorInfo(body)
	m := allowedRe.FindStringSubmatch(msg)
	if m == nil {
		return nil, false
	}
	var out []string
	for _, id := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '"' || r == '\'' }) {
		out = append(out, strings.Trim(id, "."))
	}
	return out, true
}

// Success clears failure state for the key and (key, model).
func (k *Key) Success(model string, now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.fails = 0
	k.lastOK = now
	delete(k.blockN, model)
	delete(k.blocked, model)
}

func backoff(base, max time.Duration, n int) time.Duration {
	d := base
	for i := 1; i < n && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// Verdict is the router's reaction to an upstream failure.
type Verdict struct {
	Retry      bool          // try the next candidate
	Reason     string        // short text for logs
	RetryAfter time.Duration // from the Retry-After header, if any
}

var allowedRe = regexp.MustCompile(`(?i)allowed models?:\s*(.+)$`)

// Failure applies the effects of a failed attempt and says whether to retry.
func (k *Key) Failure(model string, status int, body []byte, retryAfter time.Duration, res config.Resilience, now time.Time) Verdict {
	code, msg := errorInfo(body)
	k.mu.Lock()
	defer k.mu.Unlock()
	base := time.Duration(res.CooldownBaseSec) * time.Second
	max := time.Duration(res.CooldownMaxSec) * time.Second
	blockModel := func(d time.Duration) {
		k.blockN[model]++
		if d == 0 {
			d = backoff(base, max, k.blockN[model])
		}
		k.blocked[model] = now.Add(d)
	}
	k.lastErr = trim(code + " " + msg)
	switch {
	case status == 429 && code == "plan_limit_reached":
		k.planBlocked = now.Add(5 * time.Minute)
		return Verdict{true, "plan limit reached", 0}
	case status == 429:
		d := retryAfter
		if d <= 0 {
			k.blockN["*"]++
			d = backoff(base, max, k.blockN["*"])
		}
		if d > max {
			d = max
		}
		k.cooldown = now.Add(d)
		return Verdict{true, "rate limited", retryAfter}
	case status == 402:
		k.balanceEmpty = now.Add(10 * time.Minute)
		return Verdict{true, "insufficient balance", 0}
	case status == 401 || status == 403:
		k.cooldown = now.Add(5 * time.Minute)
		k.fails++
		return Verdict{true, "key rejected", 0}
	case status == 400 || status == 404:
		low := strings.ToLower(msg)
		if m := allowedRe.FindStringSubmatch(msg); m != nil {
			k.learned = map[string]struct{}{}
			for _, id := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '"' || r == '\'' }) {
				k.learned[strings.Trim(id, ".")] = struct{}{}
			}
			return Verdict{true, "model not allowed for key (learned allow list)", 0}
		}
		if code == "model_not_found" || strings.Contains(low, "not allowed") || strings.Contains(low, "model not found") || strings.Contains(low, "no access") {
			blockModel(10 * time.Minute)
			return Verdict{true, "model unavailable on key", 0}
		}
		return Verdict{false, "client error", 0}
	case status == 408 || status >= 500 || status == 0:
		blockModel(0)
		k.fails++
		if k.fails >= res.BreakerFailures {
			k.breaker = now.Add(time.Duration(res.BreakerOpenSec) * time.Second)
			k.fails = 0
		}
		return Verdict{true, "upstream error", 0}
	}
	return Verdict{false, "client error", 0}
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func errorInfo(body []byte) (code, msg string) {
	var e struct {
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", string(body)
	}
	if c, ok := e.Error.Code.(string); ok {
		code = c
	}
	msg = e.Error.Message
	if msg == "" {
		msg = e.Message
	}
	return
}

// Status is the dashboard view of a key.
type Status struct {
	Name        string     `json:"name"`
	Tier        string     `json:"tier"`
	Plan        string     `json:"plan"`
	Enabled     bool       `json:"enabled"`
	HasSecret   bool       `json:"has_secret"`
	RPMLimit    int        `json:"rpm_limit"`
	RPMUsed     int        `json:"rpm_used"`
	DailyLimit  int        `json:"daily_limit"`
	DailyUsed   int        `json:"daily_used"`
	CooldownEnd time.Time  `json:"cooldown_until"`
	BreakerEnd  time.Time  `json:"breaker_until"`
	PlanBlocked time.Time  `json:"plan_blocked_until"`
	Blocked     []string   `json:"blocked_models"`
	Learned     []string   `json:"learned_allow"`
	Spent       [3]float64 `json:"plan_spent"`
	Budget      [3]float64 `json:"plan_budget"`
	LastError   string     `json:"last_error"`
	LastOK      time.Time  `json:"last_ok"`
}

// Status snapshots the key for display.
func (k *Key) Status(snap *catalog.Snapshot, now time.Time) Status {
	spent, budget, _ := k.PlanUsage(snap, now)
	k.mu.Lock()
	defer k.mu.Unlock()
	rpm, daily := k.limits(snap.Tier)
	k.rpm = prune(k.rpm, now.Add(-time.Minute).UnixMilli())
	k.free = prune(k.free, now.Add(-24*time.Hour).UnixMilli())
	st := Status{Name: k.Name, Tier: k.cfg.Tier, Plan: k.cfg.Plan, Enabled: k.cfg.Enabled, HasSecret: k.Secret != "", RPMLimit: rpm, RPMUsed: len(k.rpm),
		DailyLimit: daily, DailyUsed: len(k.free), CooldownEnd: k.cooldown, BreakerEnd: k.breaker, PlanBlocked: k.planBlocked, Spent: spent, Budget: budget, LastError: k.lastErr, LastOK: k.lastOK}
	for m, until := range k.blocked {
		if now.Before(until) {
			st.Blocked = append(st.Blocked, m)
		}
	}
	for m := range k.learned {
		st.Learned = append(st.Learned, m)
	}
	sort.Strings(st.Blocked)
	sort.Strings(st.Learned)
	return st
}

// ResetState clears cooldowns, breakers and blocks (admin action).
func (k *Key) ResetState() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.cooldown, k.breaker, k.planBlocked, k.balanceEmpty = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	k.blocked, k.blockN, k.learned, k.fails = map[string]time.Time{}, map[string]int{}, nil, 0
}
