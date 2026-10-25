package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// The endpoints in this file answer in the shape kenari answers, so a client
// that talks to kenari can point at xellter and keep reading the same JSON.
// The numbers come from xellter's own request log, not from kenari: every
// request that passed through xellter is logged with the real model it was
// routed to, the tokens it used and its price at catalog rates.

// usageRange is a requested window. kenari spells the windows "24h" and "30d".
type usageRange struct {
	since time.Time
	until time.Time
}

// parseRange reads the range query parameter. The window ends now and starts
// at the beginning of the day (24h) or 30 days ago (30d).
func parseRange(q string, now time.Time) (usageRange, bool) {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "", "24h":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return usageRange{since: start, until: now}, true
	case "30d":
		return usageRange{since: now.AddDate(0, 0, -30), until: now}, true
	default:
		return usageRange{}, false
	}
}

// usageModel is one model's traffic in the window, in kenari's field names.
type usageModel struct {
	BillingKind  string `json:"billing_kind"`
	CacheWrite   int64  `json:"cache_write_prompt_tokens"`
	Cached       int64  `json:"cached_prompt_tokens"`
	CatalogMicro int64  `json:"catalog_micro_idr"`
	Completion   int64  `json:"completion_tokens"`
	CostMicro    int64  `json:"cost_micro_idr"`
	Model        string `json:"model"`
	Prompt       int64  `json:"prompt_tokens"`
	Requests     int    `json:"requests"`
}

// usageTotals is the sum over every model.
type usageTotals struct {
	CacheWrite   int64 `json:"cache_write_prompt_tokens"`
	Cached       int64 `json:"cached_prompt_tokens"`
	CatalogMicro int64 `json:"catalog_micro_idr"`
	Completion   int64 `json:"completion_tokens"`
	CostMicro    int64 `json:"cost_micro_idr"`
	Prompt       int64 `json:"prompt_tokens"`
	Requests     int   `json:"requests"`
}

// usageSummary is the whole answer: kenari's "usage.summary" object.
type usageSummary struct {
	Object string       `json:"object"`
	Start  int64        `json:"start"`
	End    int64        `json:"end"`
	Data   []usageModel `json:"data"`
	Total  usageTotals  `json:"total"`
}

// usage answers GET /v1/usage with the client's own traffic, per real model.
func (s *Server) usage(w http.ResponseWriter, r *http.Request, cl *config.Client) {
	now := s.Now()
	win, ok := parseRange(r.URL.Query().Get("range"), now)
	if !ok {
		writeErr(w, r.URL.Path, 400, "bad_request", "range must be 24h or 30d", 0)
		return
	}
	rows, err := s.st.ModelUsage(win.since, cl.Name)
	if err != nil {
		writeErr(w, r.URL.Path, 500, "internal_error", "cannot read the request log", 0)
		return
	}
	out := usageSummary{Object: "usage.summary", Start: win.since.Unix(), End: win.until.Unix(), Data: []usageModel{}}
	for _, row := range rows {
		m := usageModel{
			BillingKind: row.Billing,
			Cached:      row.Cached,
			Completion:  row.OutTok,
			Model:       row.Model,
			Prompt:      row.InTok,
			Requests:    row.Requests,
		}
		// xellter logs the price at catalog rates; kenari's catalog_micro_idr is
		// the same list-price value, and its cost_micro_idr is what was paid:
		// the full price for payg, nothing for a plan or a free model.
		m.CatalogMicro = int64(row.Cost*1e6 + 0.5)
		if row.Billing == "payg" {
			m.CostMicro = m.CatalogMicro
		}
		out.Data = append(out.Data, m)
		out.Total.CacheWrite += m.CacheWrite
		out.Total.Cached += m.Cached
		out.Total.CatalogMicro += m.CatalogMicro
		out.Total.Completion += m.Completion
		out.Total.CostMicro += m.CostMicro
		out.Total.Prompt += m.Prompt
		out.Total.Requests += m.Requests
	}
	sort.Slice(out.Data, func(i, j int) bool {
		if out.Data[i].Requests != out.Data[j].Requests {
			return out.Data[i].Requests > out.Data[j].Requests
		}
		return out.Data[i].Model < out.Data[j].Model
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// balance answers GET /v1/account/balance.
func (s *Server) balance(w http.ResponseWriter, r *http.Request) {
	st := s.accountStatus()
	available := int64(st.BalanceIDR*1e6 + 0.5)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"available_micro_idr": available,
		"balance_micro_idr":   available,
		"currency":            "IDR",
		"object":              "account.balance",
		"reserved_micro_idr":  0,
	})
}

// quotaWindow is one plan budget window in kenari's shape.
type quotaWindow struct {
	BudgetRP    float64 `json:"budget_rp"`
	RemainingRP float64 `json:"remaining_rp"`
	ResetsAt    string  `json:"resets_at"`
	UsedFrac    float64 `json:"used_frac"`
	UsedRP      float64 `json:"used_rp"`
}

// quota answers GET /v1/account/quota with the plan windows of the account.
func (s *Server) quota(w http.ResponseWriter, r *http.Request) {
	st := s.accountStatus()
	windows := map[string]quotaWindow{}
	for name, win := range st.Windows {
		windows[name] = quotaWindow{
			BudgetRP:    win.BudgetRP,
			RemainingRP: win.RemainingRP,
			ResetsAt:    win.ResetsAt.UTC().Format(time.RFC3339),
			UsedFrac:    win.UsedFrac,
			UsedRP:      win.UsedRP,
		}
	}
	out := map[string]any{"alert_pct": 50, "coupon": nil}
	if st.Plan != "" {
		out["plan"] = map[string]any{"name": st.Plan, "windows": windows}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// accountStatus is the account the client's keys belong to.
func (s *Server) accountStatus() upstream.AccountStatus {
	snap := s.cat.Snapshot()
	now := s.Now()
	accts := s.pool.Accounts()
	if len(accts) == 0 {
		return upstream.AccountStatus{}
	}
	return accts[0].Status(snap, now)
}
