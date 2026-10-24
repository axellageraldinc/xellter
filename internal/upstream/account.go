package upstream

import (
	"sync"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
)

// Window is one plan budget window as reported by kenari (GET /v1/account/quota).
type Window struct {
	BudgetRP    float64   `json:"budget_rp"`
	UsedRP      float64   `json:"used_rp"`
	RemainingRP float64   `json:"remaining_rp"`
	UsedFrac    float64   `json:"used_frac"`
	ResetsAt    time.Time `json:"resets_at"`
}

// PlanInfo is what kenari says about the account's plan. It can only be read
// with an unrestricted key; restricted keys get 403 restricted_key_not_allowed.
type PlanInfo struct {
	Name    string            `json:"name"`    // empty = the account has no active plan
	Windows map[string]Window `json:"windows"` // five_hour, week, month
	Fetched time.Time         `json:"fetched"`
}

const infoFresh = 30 * time.Minute

// Account is the shared state of all keys that belong to one kenari account.
// kenari applies rate limits, free quotas and plan budgets per account, not per
// key, so these live here and not on Key.
type Account struct {
	Name string

	mu             sync.Mutex
	spend          []store.TimedCost // estimated plan-billed spend at catalog prices
	rpm            []int64           // request times (ms), last minute
	free           []int64           // free-model request times (ms) since 00:00 UTC
	cooldown       time.Time         // rate_limit_exceeded: nothing can be sent
	freeRPMUntil   time.Time
	freeDailyUntil time.Time
	planBlocked    time.Time
	balanceEmpty   time.Time
	blockN         int
	info           *PlanInfo
	infoKey        string // key that may read quota (unrestricted), "" = none
	balanceIDR     float64
	balanceAt      time.Time
	topupThreshold float64 // balance (IDR) from which kenari lifts the free-tier limits
}

func startOfUTCDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// SetInfo stores kenari's view of the plan; key is the key that was allowed to read it.
func (a *Account) SetInfo(info *PlanInfo, key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.info, a.infoKey = info, key
	if info != nil && info.Name != "" {
		a.planBlocked = time.Time{} // fresh numbers replace our guess
	}
}

// SetTopupThreshold sets the balance (IDR) from which kenari lifts the free-tier limits.
func (a *Account) SetTopupThreshold(idr float64) {
	a.mu.Lock()
	a.topupThreshold = idr
	a.mu.Unlock()
}

// SetBalance stores the account balance in IDR.
func (a *Account) SetBalance(idr float64, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.balanceIDR, a.balanceAt = idr, at
	if idr > 0 {
		a.balanceEmpty = time.Time{}
	}
}

// QuotaKey returns the name of an unrestricted key of this account ("" if none known).
func (a *Account) QuotaKey() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.infoKey
}

// Tier is the account's effective tier: "plan" when kenari reports a plan,
// "topup" when the balance is above kenari's threshold, else "free". There is
// no configured tier (ADR 0012).
func (a *Account) Tier(now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tier(now)
}

func (a *Account) tier(now time.Time) string {
	if a.info != nil {
		if a.info.Name != "" {
			return "plan" // a plan was reported; it lasts until kenari says otherwise
		}
		if now.Sub(a.info.Fetched) < infoFresh {
			return "free" // kenari says there is no plan any more
		}
	}
	if a.balanceAt.After(time.Time{}) && a.balanceIDR >= a.topupThreshold {
		return "topup"
	}
	return "free"
}

func (a *Account) planName(now time.Time) string {
	if a.info != nil && a.info.Name != "" {
		return a.info.Name
	}
	return ""
}

func (a *Account) limits(t catalog.FreeTier, now time.Time) (rpm, daily int) {
	var tl catalog.TierLimit
	switch a.tier(now) {
	case "plan":
		tl = t.Plan
	case "topup":
		tl = t.Topup
	default:
		tl = t.Free
	}
	return tl.RPM, tl.Daily
}

func (a *Account) inScope(m *catalog.Model, snap *catalog.Snapshot, now time.Time) bool {
	if a.tier(now) != "plan" {
		return false
	}
	pl := snap.Plan(a.planName(now))
	return pl != nil && config.MatchAny(pl.Scope, m.ID)
}

func (a *Account) sums(now time.Time) (s5, sw, sm float64) {
	for _, s := range a.spend {
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

// spentSince is our own estimate of plan spend after t (to age kenari's numbers).
func (a *Account) spentSince(t, now time.Time) float64 {
	var v float64
	for _, s := range a.spend {
		if ts := time.UnixMilli(s.TS); ts.After(t) && !ts.After(now) {
			v += s.Cost
		}
	}
	return v
}

// covered reports whether the model is paid for by the account's plan right
// now. The plan is only known when kenari reported it; the numbers come from
// kenari, and our own estimate of the spend since then ages them.
func (a *Account) covered(m *catalog.Model, snap *catalog.Snapshot, now time.Time) bool {
	if now.Before(a.planBlocked) || !a.inScope(m, snap, now) {
		return false
	}
	over := func(spent, budget float64) bool { return budget > 0 && spent >= budget*0.97 }
	if a.info != nil && now.Sub(a.info.Fetched) < infoFresh && a.info.Name != "" {
		extra := a.spentSince(a.info.Fetched, now) // spend since kenari last told us
		for _, w := range a.info.Windows {
			if w.ResetsAt.After(now) && over(w.UsedRP+extra, w.BudgetRP) {
				return false
			}
		}
		return true
	}
	pl := snap.Plan(a.planName(now))
	if pl == nil {
		return false
	}
	s5, sw, sm := a.sums(now)
	return !(over(s5, pl.Budget5h) || over(sw, pl.BudgetWeek) || over(sm, pl.BudgetMon))
}

// check applies the account-wide limits. Free models and paid models are
// limited differently (free quota per minute / per UTC day; plan budget; balance).
func (a *Account) check(m *catalog.Model, snap *catalog.Snapshot, now time.Time) string {
	if now.Before(a.cooldown) {
		return Cooldown
	}
	inPlan := a.inScope(m, snap, now)
	switch {
	case m.Free:
		if now.Before(a.freeDailyUntil) {
			return DailyLimit
		}
		if now.Before(a.freeRPMUntil) {
			return RPMLimit
		}
	case inPlan && now.Before(a.planBlocked):
		return PlanLimit
	case !inPlan && now.Before(a.balanceEmpty):
		return NoBalance
	}
	rpm, daily := a.limits(snap.Tier, now)
	if rpm > 0 {
		a.rpm = prune(a.rpm, now.Add(-time.Minute).UnixMilli())
		if len(a.rpm) >= rpm {
			return RPMLimit
		}
	}
	if m.Free && daily > 0 {
		a.free = prune(a.free, startOfUTCDay(now).UnixMilli())
		if len(a.free) >= daily {
			return DailyLimit
		}
	}
	return OK
}

func (a *Account) reserve(m *catalog.Model, now time.Time) {
	a.rpm = append(a.rpm, now.UnixMilli())
	if m.Free {
		a.free = append(a.free, now.UnixMilli())
	}
}

func (a *Account) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cooldown, a.freeRPMUntil, a.freeDailyUntil, a.planBlocked, a.balanceEmpty = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
	a.blockN = 0
}

// AccountStatus is the dashboard view of an account.
type AccountStatus struct {
	Name        string            `json:"name"`
	Tier        string            `json:"tier"`
	Plan        string            `json:"plan"`
	PlanSource  string            `json:"plan_source"` // kenari (read from the API) | configured
	QuotaKey    string            `json:"quota_key"`
	Windows     map[string]Window `json:"windows"`         // from kenari when known
	Estimated   [3]float64        `json:"estimated_spend"` // our own estimate: 5h, week, month
	Budget      [3]float64        `json:"plan_budget"`
	RPMLimit    int               `json:"rpm_limit"`
	RPMUsed     int               `json:"rpm_used"`
	DailyLimit  int               `json:"daily_limit"`
	DailyUsed   int               `json:"daily_used"`
	BalanceIDR  float64           `json:"balance_idr"`
	BalanceAt   time.Time         `json:"balance_at"`
	Cooldown    time.Time         `json:"cooldown_until"`
	FreeBlocked time.Time         `json:"free_blocked_until"`
	PlanBlocked time.Time         `json:"plan_blocked_until"`
}

// Status snapshots the account for display.
func (a *Account) Status(snap *catalog.Snapshot, now time.Time) AccountStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	rpm, daily := a.limits(snap.Tier, now)
	a.rpm = prune(a.rpm, now.Add(-time.Minute).UnixMilli())
	a.free = prune(a.free, startOfUTCDay(now).UnixMilli())
	st := AccountStatus{Name: a.Name, Tier: a.tier(now), Plan: a.planName(now), PlanSource: "detected", QuotaKey: a.infoKey,
		RPMLimit: rpm, RPMUsed: len(a.rpm), DailyLimit: daily, DailyUsed: len(a.free), BalanceIDR: a.balanceIDR, BalanceAt: a.balanceAt,
		Cooldown: a.cooldown, PlanBlocked: a.planBlocked}
	st.FreeBlocked = a.freeDailyUntil
	if a.freeRPMUntil.After(st.FreeBlocked) {
		st.FreeBlocked = a.freeRPMUntil
	}
	if a.info != nil && a.info.Name != "" {
		st.PlanSource, st.Windows = "kenari", a.info.Windows
	}
	if pl := snap.Plan(st.Plan); pl != nil {
		st.Estimated[0], st.Estimated[1], st.Estimated[2] = a.sums(now)
		st.Budget = [3]float64{pl.Budget5h, pl.BudgetWeek, pl.BudgetMon}
	}
	return st
}
