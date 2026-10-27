// Package upstream models kenari API keys and the accounts they belong to:
// account-wide rate limits, free quota and plan budgets (account.go), and
// per-key cooldowns, circuit breaker and allow lists (this file). Everything is
// in memory and seeded from the request log at startup.
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

// Key is one upstream API key with its own failure state. Limits that kenari
// applies per account are on its Account.
type Key struct {
	Name string // unique id: "<client>/<key name>", or "account" for the read-only account key

	mu       sync.Mutex
	label    string // the key name inside its client
	client   string // owner client ("" for the account key)
	secret   string
	cfg      config.Upstream
	acct     *Account
	readOnly bool      // the account key: it reads plan and balance, it never serves requests
	capable  string    // the kenari service this key is dedicated to ("" = a model key, "search" = the X search endpoint)
	cooldown time.Time // key unusable until (rejected key)
	fails    int       // consecutive failures
	breaker  time.Time
	blocked  map[string]time.Time // model -> until
	blockN   map[string]int
	learned  map[string]struct{} // models this key may use (probed / learned from 400s); nil = unrestricted
	probed   bool                // the allow list above is known (a probe succeeded)
	lastErr  string
	lastOK   time.Time
}

// Pool is the set of keys and accounts.
type Pool struct {
	mu    sync.RWMutex
	keys  []*Key
	by    map[string]*Key
	accts map[string]*Account
	rr    atomic.Uint64
}

// NewPool returns an empty pool.
func NewPool() *Pool { return &Pool{by: map[string]*Key{}, accts: map[string]*Account{}} }

// DefaultAccount is the name of the one kenari account: there is a single
// account, and the limits kenari applies are per account.
const DefaultAccount = "main"

// Reload applies a new config; state of keys and accounts with unchanged names
// is kept. st may be nil (tests); it seeds counters of brand-new keys.
func (p *Pool) Reload(cfg *config.Config, secrets map[string]string, st *store.Store) {
	p.mu.Lock()
	defer p.mu.Unlock()
	type entry struct {
		id, client string
		u          config.Upstream
		readOnly   bool
	}
	var entries []entry
	for _, cl := range cfg.Clients {
		for _, u := range cl.Keys {
			entries = append(entries, entry{cl.Name + "/" + u.Name, cl.Name, u, false})
		}
	}
	if env := cfg.Kenari.AccountKeyEnv; env != "" && secrets[env] != "" {
		// the account key only reads plan quota and balance; it never serves requests
		entries = append(entries, entry{"account", "", config.Upstream{Name: "account", KeyEnv: env}, true})
	}
	next := make([]*Key, 0, len(entries))
	by := map[string]*Key{}
	accts := map[string]*Account{}
	now := time.Now()
	for _, e := range entries {
		u := e.u
		k := p.by[e.id]
		isNew := k == nil
		if isNew {
			k = &Key{Name: e.id, blocked: map[string]time.Time{}, blockN: map[string]int{}}
		}
		// every key belongs to the same kenari account: the limits kenari applies
		// are per account, and there is only one account.
		a := accts[DefaultAccount]
		if a == nil {
			a = p.accts[DefaultAccount]
			if a == nil {
				a = &Account{Name: DefaultAccount}
			}
			accts[DefaultAccount] = a
		}
		if isNew && st != nil {
			// the account's counters are read by request handlers while this runs, so they are seeded under
			// its lock, like every other write to them
			var free, rpm []int64
			var spend []store.TimedCost
			if t, err := st.FreeTimes(e.id, startOfUTCDay(now)); err == nil {
				free = t
			}
			if t, err := st.AllTimes(e.id, now.Add(-time.Minute)); err == nil {
				rpm = t
			}
			if sp, err := st.PlanSpend(e.id, now.Add(-30*24*time.Hour)); err == nil {
				spend = sp
			}
			a.mu.Lock()
			a.free = append(a.free, free...)
			a.rpm = append(a.rpm, rpm...)
			a.spend = append(a.spend, spend...)
			a.mu.Unlock()
		}
		k.mu.Lock()
		k.cfg, k.secret, k.acct, k.label, k.client, k.readOnly, k.capable = u, secrets[u.KeyEnv], a, u.Name, e.client, e.readOnly, u.Capability
		k.mu.Unlock()
		next = append(next, k)
		by[e.id] = k
	}
	p.keys, p.by, p.accts = next, by, accts
}

// ClientKeys returns the keys that belong to a client and may serve a model
// call. A key dedicated to a capability (the search endpoint) is not one of
// them: it never takes part in model routing.
func (p *Pool) ClientKeys(client string) []*Key {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*Key
	for _, k := range p.keys {
		if k.Owner() == client && client != "" && k.Capability() == "" {
			out = append(out, k)
		}
	}
	return out
}

// ClientCapabilityKeys returns the keys of a client that are dedicated to one
// capability, in configuration order.
func (p *Pool) ClientCapabilityKeys(client, capability string) []*Key {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*Key
	for _, k := range p.keys {
		if k.Owner() == client && client != "" && k.Capability() == capability {
			out = append(out, k)
		}
	}
	return out
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

// Accounts returns the current accounts sorted by name.
func (p *Pool) Accounts() []*Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Account, 0, len(p.accts))
	for _, a := range p.accts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Next returns a monotonically increasing counter for round-robin rotation.
func (p *Pool) Next() uint64 { return p.rr.Add(1) }

// Secret returns the kenari key value ("" when the secret is not configured).
func (k *Key) Secret() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.secret
}

// Label returns the key's name inside its client.
func (k *Key) Label() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.label
}

// Owner returns the client the key belongs to ("" for the account key).
func (k *Key) Owner() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.client
}

// Config returns the key's config.
func (k *Key) Config() config.Upstream {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cfg
}

// ReadOnly reports whether the key is the dashboard's own account key: it
// reads plan and balance and never serves client traffic.
func (k *Key) ReadOnly() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.readOnly
}

// Capability reports the kenari service this key is dedicated to ("" = a model
// key). A key with a capability is never probed and never serves a model call.
func (k *Key) Capability() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.capable
}

// Account returns the key's account.
func (k *Key) Account() *Account {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.acct
}

func prune(ts []int64, cut int64) []int64 {
	i := 0
	for i < len(ts) && ts[i] < cut {
		i++
	}
	return ts[i:]
}

// AllowedModel reports whether the learned list permits the model. The list
// comes from kenari itself (a probe or a 400 answer); it is never configured.
func (k *Key) AllowedModel(id string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.allowed(id)
}

func (k *Key) allowed(id string) bool {
	if k.learned != nil {
		_, ok := k.learned[id]
		return ok
	}
	return true
}

// Covered reports whether the model is billed to the account's plan right now.
func (k *Key) Covered(m *catalog.Model, snap *catalog.Snapshot, now time.Time) bool {
	a := k.Account()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.covered(m, snap, now)
}

// FreeCache reports whether cache tokens of the model are free on the account's plan.
func (k *Key) FreeCache(m *catalog.Model, snap *catalog.Snapshot, now time.Time) bool {
	a := k.Account()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.covered(m, snap, now) {
		return false
	}
	pl := snap.Plan(a.planName(now))
	return pl != nil && config.MatchAny(pl.FreeCache, m.ID)
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
	if r := k.check(m, now); r != OK {
		return r
	}
	a := k.acct
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.check(m, snap, now)
}

// State reports why a key cannot serve anything at all: no secret, cooling down, or its breaker open. It
// deliberately does not look at models or per-model limits, because the caller asks it when no model could
// be attempted and needs to know whether the key itself is the problem.
func (k *Key) State(now time.Time) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch {
	case k.secret == "":
		return NoSecret
	case now.Before(k.cooldown):
		return Cooldown
	case now.Before(k.breaker):
		return BreakerOpen
	}
	a := k.acct
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Before(a.cooldown) {
		return Cooldown
	}
	return OK
}

func (k *Key) check(m *catalog.Model, now time.Time) string {
	switch {
	case k.secret == "":
		return NoSecret
	case now.Before(k.cooldown):
		return Cooldown
	case now.Before(k.breaker):
		return BreakerOpen
	}
	if m == nil { // a state query, not a per-model check
		return OK
	}
	if !k.allowed(m.ID) {
		return NotAllowed
	}
	if until, ok := k.blocked[m.ID]; ok && now.Before(until) {
		return Blocked
	}
	return OK
}

// Reserve checks and, if usable, counts the request against the account limits.
func (k *Key) Reserve(m *catalog.Model, snap *catalog.Snapshot, now time.Time) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if r := k.check(m, now); r != OK {
		return r
	}
	a := k.acct
	a.mu.Lock()
	defer a.mu.Unlock()
	if r := a.check(m, snap, now); r != OK {
		return r
	}
	a.reserve(m, now)
	return OK
}

// RecordSpend adds plan-billed catalog cost to the account's estimate.
func (k *Key) RecordSpend(ts time.Time, cost float64) {
	a := k.Account()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.spend = append(a.spend, store.TimedCost{TS: ts.UnixMilli(), Cost: cost})
	cut := ts.Add(-31 * 24 * time.Hour).UnixMilli()
	i := 0
	for i < len(a.spend) && a.spend[i].TS < cut {
		i++
	}
	a.spend = a.spend[i:]
}

// SetAllowed replaces the learned allow list (nil = unrestricted).
func (k *Key) SetAllowed(models []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.probed = true
	if models == nil {
		k.learned = nil
		return
	}
	k.learned = make(map[string]struct{}, len(models))
	for _, m := range models {
		k.learned[m] = struct{}{}
	}
}

// Probed reports whether the key's model access has been determined.
func (k *Key) Probed() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.probed
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
	k.fails = 0
	k.lastOK = now
	k.lastErr = "" // a success makes the previous failure history, not current state
	delete(k.blockN, model)
	delete(k.blocked, model)
	a := k.acct
	k.mu.Unlock()
	a.mu.Lock()
	a.blockN = 0
	a.mu.Unlock()
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

// Key health is not configurable: a key that keeps failing is a key that is down, and the numbers below are
// what makes the gateway stop using it for a while without touching a preset's ranking (ADR 0020).
const (
	cooldownBase = 30 * time.Second
	cooldownMax  = 15 * time.Minute
	breakerFails = 5
	breakerOpen  = 2 * time.Minute
)

// Failure applies the effects of a failed attempt and says whether to retry.
// Error codes follow https://kenari.id/docs/rate-limits.
func (k *Key) Failure(model string, status int, body []byte, retryAfter time.Duration, now time.Time) Verdict {
	code, msg := errorInfo(body)
	k.mu.Lock()
	defer k.mu.Unlock()
	a := k.acct
	base, max := cooldownBase, cooldownMax
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
		a.mu.Lock()
		a.planBlocked = now.Add(5 * time.Minute)
		a.mu.Unlock()
		return Verdict{true, "plan limit reached", 0}
	case status == 429 && code == "free_quota_daily":
		a.mu.Lock()
		a.freeDailyUntil = startOfUTCDay(now).Add(24 * time.Hour)
		a.mu.Unlock()
		return Verdict{true, "free daily quota used (resets 00:00 UTC)", 0}
	case status == 429 && code == "free_quota_rpm":
		d := retryAfter
		if d <= 0 {
			d = 60 * time.Second
		}
		a.mu.Lock()
		a.freeRPMUntil = now.Add(d)
		a.mu.Unlock()
		return Verdict{true, "free quota per minute", d}
	case status == 429 && code == "upstream_error":
		blockModel(0) // the provider behind this model is rate limiting
		return Verdict{true, "provider rate limited", 0}
	case status == 429:
		d := retryAfter
		a.mu.Lock()
		if d <= 0 {
			a.blockN++
			d = backoff(base, max, a.blockN)
		}
		if d > max {
			d = max
		}
		a.cooldown = now.Add(d) // kenari limits per account: every key of it waits
		a.mu.Unlock()
		return Verdict{true, "rate limited", retryAfter}
	case status == 402:
		a.mu.Lock()
		a.balanceEmpty = now.Add(10 * time.Minute)
		a.mu.Unlock()
		return Verdict{true, "insufficient balance", 0}
	case status == 401 || status == 403:
		k.cooldown = now.Add(5 * time.Minute)
		k.fails++
		return Verdict{true, "key rejected", 0}
	case status == 400 || status == 404:
		low := strings.ToLower(msg)
		if list, ok := ParseAllowed(body); ok {
			k.learned = make(map[string]struct{}, len(list))
			for _, id := range list {
				k.learned[id] = struct{}{}
			}
			return Verdict{true, "model not allowed for key (learned allow list)", 0}
		}
		if code == "model_not_found" || strings.Contains(low, "not allowed") || strings.Contains(low, "model not found") || strings.Contains(low, "no access") {
			blockModel(10 * time.Minute)
			return Verdict{true, "model unavailable on key", 0}
		}
		// A context that does not fit is the caller's history, not a fault of the
		// model. It must not count towards the model's failure record: evicting a
		// healthy model because one request was too long would take it out of the
		// preset for a reason that has nothing to do with the model (ADR 0020,
		// ADR 0026). The retry is refused too, because the same request will not
		// fit on the next key either.
		if code == "context_length_exceeded" || strings.Contains(low, "maximum context length") {
			return Verdict{false, "context length exceeded", 0}
		}
		return Verdict{false, "client error", 0}
	case status == 0:
		// A transport failure says nothing about one model, so it counts towards the key's breaker: after a few
		// in a row the key is treated as down for everyone.
		k.fails++
		if k.fails >= breakerFails {
			k.breaker = now.Add(breakerOpen)
			k.fails = 0
		}
		return Verdict{true, "network error", 0}
	case status == 408 || status >= 500:
		// A server error is attributable to the model or its provider, so it blocks that model only. The
		// preset's own eviction rule (three errors in an hour) is what takes a model out of a preset; letting
		// one bad model trip the key's breaker would take every healthy model down with it (ADR 0020).
		blockModel(0)
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
	Name         string    `json:"name"`
	Label        string    `json:"label"`
	Client       string    `json:"client"`
	Account      string    `json:"account"`
	Tier         string    `json:"tier"`
	HasSecret    bool      `json:"has_secret"`
	CooldownEnd  time.Time `json:"cooldown_until"`
	BreakerEnd   time.Time `json:"breaker_until"`
	Blocked      []string  `json:"blocked_models"`
	Learned      []string  `json:"learned_allow"`
	LastError    string    `json:"last_error"`
	LastOK       time.Time `json:"last_ok"`
	Unrestricted bool      `json:"unrestricted"` // may read account quota and balance
}

// Status snapshots the key for display.
func (k *Key) Status(now time.Time) Status {
	k.mu.Lock()
	a := k.acct
	st := Status{Name: k.Name, Label: k.label, Client: k.client, Account: a.Name, Tier: a.tier(now), HasSecret: k.secret != "", CooldownEnd: k.cooldown, BreakerEnd: k.breaker, LastError: k.lastErr, LastOK: k.lastOK}
	for m, until := range k.blocked {
		if now.Before(until) {
			st.Blocked = append(st.Blocked, m)
		}
	}
	for m := range k.learned {
		st.Learned = append(st.Learned, m)
	}
	name := k.Name
	k.mu.Unlock()
	st.Unrestricted = a.QuotaKey() == name
	sort.Strings(st.Blocked)
	sort.Strings(st.Learned)
	return st
}

// ResetState clears cooldowns, breakers and blocks of the key and its account.
func (k *Key) ResetState() {
	k.mu.Lock()
	k.cooldown, k.breaker = time.Time{}, time.Time{}
	k.blocked, k.blockN, k.learned, k.fails, k.probed = map[string]time.Time{}, map[string]int{}, nil, 0, false
	a := k.acct
	k.mu.Unlock()
	a.reset()
}
