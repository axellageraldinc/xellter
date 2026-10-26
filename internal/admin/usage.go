package admin

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/axellageraldinc/xellter/internal/rank"
)

// The Usage page drills down one level at a time: every client, then the
// presets of one client, then the models of one preset. Every level is the same
// shape, so the panel renders one table and one set of totals for all three.
//
// GET /admin/api/usage/drill?window=24h&client=&preset=
//
//	{
//	  "window": "24h", "since": "...", "client": "", "preset": "",
//	  "level": "client" | "preset" | "model",
//	  "totals": usageTotals,
//	  "rows": [{ "key", "label", "client", "preset", "model", "billing", "billing_label",
//	             "requests", "errors", "in_tok", "out_tok", "cached_tok", "rtk_saved", "cost_idr" }],
//	  "series": [UsageRow...],   // hourly buckets, for the chart
//	  "kenari": { ... }          // kenari's own plan windows and balance
//	}

// usageTotals is the sum of the rows on one level.
type usageTotals struct {
	Requests int     `json:"requests"`
	Errors   int     `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Cached   int64   `json:"cached_tok"`
	RTKSaved int64   `json:"rtk_saved"`
	Cost     float64 `json:"cost_idr"`
}

// usageRow is one line of the drill-down.
type usageRow struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	Client       string  `json:"client,omitempty"`
	Preset       string  `json:"preset,omitempty"`
	Model        string  `json:"model,omitempty"`
	Billing      string  `json:"billing,omitempty"`
	BillingLabel string  `json:"billing_label,omitempty"`
	Requests     int     `json:"requests"`
	Errors       int     `json:"errors"`
	InTok        int64   `json:"in_tok"`
	OutTok       int64   `json:"out_tok"`
	Cached       int64   `json:"cached_tok"`
	RTKSaved     int64   `json:"rtk_saved"`
	Cost         float64 `json:"cost_idr"`
}

// drillWindow reads the window query parameter. The page offers 24 h and 30 d;
// any whole number of hours is accepted so a link can carry its own window.
func drillWindow(q string, now time.Time) (string, time.Duration, bool) {
	switch q {
	case "", "24h":
		return "24h", 24 * time.Hour, true
	case "30d":
		return "30d", 30 * 24 * time.Hour, true
	}
	if h, err := strconv.Atoi(q); err == nil && h > 0 && h <= 24*90 {
		return q + "h", time.Duration(h) * time.Hour, true
	}
	return "", 0, false
}

func (h *Handler) usageDrill(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now()
	window, span, ok := drillWindow(q.Get("window"), now)
	if !ok {
		jerr(w, 400, "window must be 24h or 30d")
		return
	}
	client, preset := q.Get("client"), q.Get("preset")
	if client != "" && findClient(h.a.Cfg(), client) == nil {
		jerr(w, 404, "no such client")
		return
	}
	since := now.Add(-span)
	// The chart buckets come from the same window but are rounded to whole
	// buckets, so a partial hour never looks like a full one.
	unit := "hour"
	if span > 72*time.Hour {
		unit = "day"
	}
	series, err := h.a.St.Usage(unit, ceilTo(since, map[string]time.Duration{"hour": time.Hour, "day": 24 * time.Hour}[unit]), client, preset)
	if err != nil {
		jerr(w, 500, "cannot read the request log")
		return
	}
	out := map[string]any{
		"window": window, "since": since, "client": client, "preset": preset,
		"series": series, "kenari": h.kenariStrip(now),
	}
	var rows []usageRow
	switch {
	case preset != "":
		// the models inside one preset
		raw, err := h.a.St.Usage("preset_model", since, client, preset)
		if err != nil {
			jerr(w, 500, "cannot read the request log")
			return
		}
		// The log groups by preset and model, not by billing class, so the class
		// comes from the live ranking: it is what this client pays for the model
		// right now, which is what the column means.
		billing := h.modelBilling(client, preset)
		for _, u := range raw {
			b := u.Billing
			if b == "" {
				b = billing[u.Model]
			}
			rows = append(rows, usageRow{
				Key: u.Model, Label: u.Model, Client: client, Preset: u.Preset, Model: u.Model,
				Billing: b, BillingLabel: billingLabel(b),
				Requests: u.Requests, Errors: u.Errors, InTok: u.InTok, OutTok: u.OutTok,
				Cached: u.Cached, RTKSaved: u.RTKSaved, Cost: u.Cost,
			})
		}
		out["level"] = "model"
	case client != "":
		// the presets of one client
		raw, err := h.a.St.Usage("preset", since, client, "")
		if err != nil {
			jerr(w, 500, "cannot read the request log")
			return
		}
		cfg := h.a.Cfg()
		cl := findClient(cfg, client)
		known := map[string]bool{}
		if cl != nil {
			for name := range cl.Presets {
				known[name] = true
			}
		}
		for _, u := range raw {
			rows = append(rows, usageRow{
				Key: u.Preset, Label: u.Preset, Client: client, Preset: u.Preset,
				Requests: u.Requests, Errors: u.Errors, InTok: u.InTok, OutTok: u.OutTok,
				Cached: u.Cached, RTKSaved: u.RTKSaved, Cost: u.Cost,
			})
			delete(known, u.Preset)
		}
		// a preset with no traffic still belongs on the page: zero, not missing
		for name := range known {
			rows = append(rows, usageRow{Key: name, Label: name, Client: client, Preset: name})
		}
		out["level"] = "preset"
	default:
		// every client
		raw, err := h.a.St.Usage("client", since, "", "")
		if err != nil {
			jerr(w, 500, "cannot read the request log")
			return
		}
		cfg := h.a.Cfg()
		seen := map[string]bool{}
		for _, u := range raw {
			rows = append(rows, usageRow{
				Key: u.Client, Label: u.Client, Client: u.Client,
				Requests: u.Requests, Errors: u.Errors, InTok: u.InTok, OutTok: u.OutTok,
				Cached: u.Cached, RTKSaved: u.RTKSaved, Cost: u.Cost,
			})
			seen[u.Client] = true
		}
		for _, cl := range cfg.Clients {
			if !seen[cl.Name] {
				rows = append(rows, usageRow{Key: cl.Name, Label: cl.Name, Client: cl.Name})
			}
		}
		out["level"] = "client"
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Requests != rows[j].Requests {
			return rows[i].Requests > rows[j].Requests
		}
		return rows[i].Key < rows[j].Key
	})
	var t usageTotals
	for _, x := range rows {
		t.Requests += x.Requests
		t.Errors += x.Errors
		t.InTok += x.InTok
		t.OutTok += x.OutTok
		t.Cached += x.Cached
		t.RTKSaved += x.RTKSaved
		t.Cost += x.Cost
	}
	out["rows"], out["totals"] = rows, t
	jok(w, out)
}

// modelBilling maps every model of one preset to how this client pays for it.
func (h *Handler) modelBilling(client, preset string) map[string]string {
	out := map[string]string{}
	if rk := h.a.Rt.Rankings(); rk != nil {
		if x := rk[rank.Key(client, preset)]; x != nil {
			for _, c := range x.Candidates {
				out[c.Model] = c.Billing
			}
		}
	}
	cfg, snap, now := h.a.Cfg(), h.a.Cat.Snapshot(), time.Now()
	cl := findClient(cfg, client)
	if cl == nil || snap == nil {
		return out
	}
	sh, ok := cl.Presets[preset]
	if !ok {
		return out
	}
	for _, id := range sh.Models {
		if _, known := out[id]; known {
			continue
		}
		if m := snap.Models[id]; m != nil {
			b, _ := h.billingOf(cl, m, snap, now)
			out[id] = b
		}
	}
	return out
}

func billingLabel(b string) string {
	switch b {
	case "free":
		return "free"
	case "plan":
		return "plan"
	case "balance", "payg", "":
		return "balance"
	}
	return b
}

// kenariStrip is kenari's own view: the plan windows and the balance, next to
// what xellter's log says. Requirement 19.
func (h *Handler) kenariStrip(now time.Time) map[string]any {
	snap := h.a.Cat.Snapshot()
	accts := h.a.Pool.Accounts()
	out := map[string]any{"configured": len(accts) > 0}
	if len(accts) == 0 {
		return out
	}
	st := accts[0].Status(snap, now)
	out["account"] = st
	windows := make([]map[string]any, 0, len(st.Windows))
	for name, w := range st.Windows {
		windows = append(windows, map[string]any{
			"name": name, "label": windowName(name), "used_idr": w.UsedRP, "budget_idr": w.BudgetRP,
			"remaining_idr": w.RemainingRP, "used_frac": w.UsedFrac, "resets_at": w.ResetsAt,
		})
	}
	sort.Slice(windows, func(i, j int) bool {
		return winRank(windows[i]["name"].(string)) < winRank(windows[j]["name"].(string))
	})
	out["windows"] = windows
	return out
}

func winRank(n string) int {
	switch n {
	case "five_hour", "5h":
		return 0
	case "week":
		return 1
	case "month":
		return 2
	}
	return 9
}

// ---- sessions ----

// sessionInfo is one live session of a client: the model it is pinned to, what
// it has spent, and which models it has seen. GET /admin/api/session?client=&id=
func (h *Handler) sessionInfo(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, id := q.Get("client"), q.Get("id")
	if client == "" || id == "" {
		jerr(w, 400, "client and id are required")
		return
	}
	cl := findClient(h.a.Cfg(), client)
	if cl == nil {
		jerr(w, 404, "no such client")
		return
	}
	u, err := h.a.St.Session(client, id)
	if err != nil {
		jerr(w, 500, "cannot read the request log")
		return
	}
	if u == nil {
		jerr(w, 404, "this session has no traffic yet")
		return
	}
	out := map[string]any{"session": u, "pins": map[string]any{}}
	now := time.Now()
	pins := map[string]any{}
	for name := range cl.Presets {
		if model, at := h.a.Rt.PinOf(client, name, id); model != "" {
			pins[name] = map[string]any{"model": model, "pinned_at": at, "live_for_s": int(now.Sub(at).Seconds())}
		}
	}
	out["pins"] = pins
	jok(w, out)
}

// usageKeys is per-key traffic of one client, the dimension /v1/usage/keys
// exposes to the client itself.
func (h *Handler) usageKeys(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	if client == "" {
		jerr(w, 400, "client is required")
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	rows, err := h.a.St.Usage("key", time.Now().Add(-time.Duration(hours)*time.Hour), client, "")
	if err != nil {
		jerr(w, 500, "cannot read the request log")
		return
	}
	jok(w, rows)
}
