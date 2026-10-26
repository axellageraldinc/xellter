package admin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/proxy"
	"github.com/axellageraldinc/xellter/internal/rank"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// ---- overview ----

type alert struct {
	Level  string `json:"level"` // info | warn | bad
	Text   string `json:"text"`
	Client string `json:"client,omitempty"` // client page to open
}

type presetRow struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Modality    string   `json:"modality"`
	Now         string   `json:"now"`
	Flags       []string `json:"flags"`
	Next        []string `json:"next"`
	Models      int      `json:"models"` // distinct models this client's keys can call for the preset
}

type clientRow struct {
	Name       string            `json:"name"`
	Enabled    bool              `json:"enabled"`
	KeyHint    string            `json:"key_hint"`
	Keys       []upstream.Status `json:"keys"`
	Presets    []presetRow       `json:"presets"`
	SpentToday float64           `json:"spent_today"`
	Budget     float64           `json:"budget"`
	Requests   int               `json:"requests_24h"`
	Errors     int               `json:"errors_24h"`
	Cost       float64           `json:"cost_24h"`
	RTKSaved   int64             `json:"rtk_saved_24h"`
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	snap := h.a.Cat.Snapshot()
	now := time.Now()
	cfg := h.a.Cfg()
	out := map[string]any{
		"uptime_s": int(now.Sub(h.a.Started).Seconds()),
		"requests": map[string]any{"total": h.a.Px.Total.Load(), "failed": h.a.Px.Failed.Load(), "inflight": h.a.Px.Inflight.Load(), "rtk_saved_chars": h.a.Px.RTKSaved.Load()},
	}
	var accts []upstream.AccountStatus
	statuses := map[string][]upstream.Status{}
	var accountKey *upstream.Status
	if snap != nil {
		out["catalog"] = map[string]any{"models": len(snap.Models), "age_s": int(now.Sub(snap.Fetched).Seconds()), "degraded": snap.Degraded, "plans": len(snap.Plans), "tier": snap.Tier, "usd_idr": snap.USDIDR}
		for _, k := range h.a.Pool.Keys() {
			st := k.Status(now)
			if st.Client == "" {
				accountKey = &st
			} else {
				statuses[st.Client] = append(statuses[st.Client], st)
			}
		}
		for _, a := range h.a.Pool.Accounts() {
			accts = append(accts, a.Status(snap, now))
		}
		var plans []string
		for _, p := range snap.Plans {
			plans = append(plans, p.Name)
		}
		out["plan_names"] = plans
	}
	out["accounts"] = accts
	out["account_key"] = map[string]any{"configured": accountKey != nil, "status": accountKey, "env": cfg.Kenari.AccountKeyEnv}
	usage := map[string]clientRow{}
	if rows, err := h.a.St.Usage("client", now.Add(-24*time.Hour), ""); err == nil {
		for _, u := range rows {
			usage[u.Key] = clientRow{Requests: u.Requests, Errors: u.Errors, Cost: u.Cost, RTKSaved: u.RTKSaved}
		}
	}
	rows := make([]clientRow, 0, len(cfg.Clients))
	for _, c := range cfg.Clients {
		u := usage[c.Name]
		rows = append(rows, clientRow{Name: c.Name, Enabled: c.Enabled, KeyHint: c.KeyHint, Keys: statuses[c.Name], Presets: h.presetRows(&c), SpentToday: h.a.Px.SpentToday(c.Name), Budget: c.DailyBudget, Requests: u.Requests, Errors: u.Errors, Cost: u.Cost, RTKSaved: u.RTKSaved})
	}
	out["clients"] = rows
	out["alerts"], out["status"] = h.alerts(snap, rows, accts, now)
	jok(w, out)
}

func (h *Handler) presetRows(cl *config.Client) []presetRow {
	rk := h.a.Rt.Rankings()
	names := make([]string, 0, len(cl.Presets))
	for n := range cl.Presets {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return presetOrder(cl.Presets[names[i]].Modality, names[i]) < presetOrder(cl.Presets[names[j]].Modality, names[j])
	})
	var rows []presetRow
	for _, n := range names {
		p := cl.Presets[n]
		r := presetRow{Name: n, Description: p.Description, Modality: p.Modality}
		if x := rk[rank.Key(cl.Name, n)]; x != nil {
			r.Models = x.Callable
			for i, c := range x.Candidates {
				switch {
				case i == 0:
					r.Now, r.Flags = c.Model, c.Flags
				case i <= 2:
					r.Next = append(r.Next, c.Model)
				}
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// presetOrder sorts presets by kind (text, image, video, ...), the generated one first.
func presetOrder(modality, name string) string {
	for i, m := range config.PresetModalities {
		if m == modality {
			if name == config.PresetName(m) {
				return fmt.Sprintf("%d0", i)
			}
			return fmt.Sprintf("%d1%s", i, name)
		}
	}
	return "9" + name
}

func (h *Handler) alerts(snap *catalog.Snapshot, clients []clientRow, accts []upstream.AccountStatus, now time.Time) ([]alert, string) {
	var out []alert
	add := func(level, text, client string) { out = append(out, alert{level, text, client}) }
	if snap == nil {
		add("bad", "Model catalog not loaded yet.", "")
	} else if snap.Degraded {
		add("warn", "kenari is unreachable: serving from the last known catalog.", "")
	}
	if len(clients) == 0 {
		add("info", "No clients yet. Add one, then give it a kenari key.", "")
	}
	for _, c := range clients {
		active := 0
		for _, k := range c.Keys {
			switch {
			case !k.HasSecret:
				add("bad", fmt.Sprintf("%s: key \"%s\" has no key value.", c.Name, k.Label), c.Name)
			case k.CooldownEnd.After(now) || k.BreakerEnd.After(now):
				add("warn", fmt.Sprintf("%s: key \"%s\" is paused after errors%s.", c.Name, k.Label, lastErr(k.LastError)), c.Name)
				active++
			default:
				active++
			}
		}
		if len(c.Keys) == 0 {
			add("info", fmt.Sprintf("%s has no kenari key yet, so it cannot serve requests.", c.Name), c.Name)
		}
		for _, p := range c.Presets {
			if p.Now == "" && len(c.Keys) > 0 {
				add("warn", fmt.Sprintf("%s: preset \"%s\" has no usable model right now.", c.Name, p.Name), c.Name)
			}
		}
	}
	for _, a := range accts {
		if a.QuotaKey == "" && a.Plan != "" {
			add("info", "Plan usage is estimated. Add the account key (Overview, Kenari plan) to read the real numbers.", "")
		}
		for name, w := range a.Windows {
			switch {
			case w.UsedFrac >= 0.97:
				add("bad", fmt.Sprintf("Plan %s budget is %.0f%% used; plan models are paused until it resets.", windowName(name), w.UsedFrac*100), "")
			case w.UsedFrac >= 0.8:
				add("warn", fmt.Sprintf("Plan %s budget is %.0f%% used.", windowName(name), w.UsedFrac*100), "")
			}
		}
		if a.Cooldown.After(now) {
			add("warn", "kenari is rate limiting the account; requests wait or move to other models.", "")
		}
		if a.FreeBlocked.After(now) {
			add("warn", "Free models are paused (free quota used).", "")
		}
		if a.PlanBlocked.After(now) {
			add("warn", "kenari reported the plan limit; plan models are paused for a few minutes.", "")
		}
	}
	status := "ok"
	for _, a := range out {
		if a.Level == "bad" {
			status = "bad"
		} else if a.Level == "warn" && status == "ok" {
			status = "warn"
		}
	}
	return out, status
}

func windowName(n string) string {
	switch n {
	case "five_hour":
		return "5-hour"
	case "week":
		return "weekly"
	case "month":
		return "monthly"
	}
	return n
}

func lastErr(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

// ---- clients ----

func newKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "xl-" + hex.EncodeToString(b)
}

func cloneConfig(c *config.Config) config.Config {
	next := *c
	next.Clients = make([]config.Client, len(c.Clients))
	for i, cl := range c.Clients {
		cl.Keys = append([]config.Upstream(nil), cl.Keys...)
		cl.Presets = clonePresets(cl.Presets)
		cl.Seeded = append([]string(nil), cl.Seeded...)
		next.Clients[i] = cl
	}
	return next
}

func clonePresets(m map[string]config.Preset) map[string]config.Preset {
	out := make(map[string]config.Preset, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (h *Handler) clients(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"` // create | rotate | delete
		Name   string `json:"name"`
		Note   string `json:"note"`
	}
	if r.Method != http.MethodPost || readJSON(r, &in) != nil {
		jerr(w, 400, "POST {action, name}")
		return
	}
	in.Name = strings.Join(strings.Fields(in.Name), "-")
	next := cloneConfig(h.a.Cfg())
	key := newKey()
	hash, hint := proxy.HashKey(key), "…"+key[len(key)-4:]
	switch in.Action {
	case "create":
		next.Clients = append(next.Clients, config.Client{Name: in.Name, Enabled: true, KeyHash: hash, KeyHint: hint, Note: in.Note, Presets: map[string]config.Preset{}})
	case "rotate":
		found := false
		for i := range next.Clients {
			if next.Clients[i].Name == in.Name {
				next.Clients[i].KeyHash, next.Clients[i].KeyHint, found = hash, hint, true
			}
		}
		if !found {
			jerr(w, 404, "no such client")
			return
		}
	case "delete":
		kept := next.Clients[:0:0]
		for _, c := range next.Clients {
			if c.Name == in.Name {
				for _, k := range c.Keys {
					_ = config.DeleteSecret(next.Server.SecretsFile, k.KeyEnv)
				}
				continue
			}
			kept = append(kept, c)
		}
		next.Clients = kept
		key = ""
	default:
		jerr(w, 400, "action must be create, rotate or delete")
		return
	}
	if err := config.Normalize(&next); err != nil {
		jerr(w, 422, err.Error())
		return
	}
	if err := h.a.Apply(&next); err != nil {
		jerr(w, 422, err.Error())
		return
	}
	jok(w, map[string]any{"ok": true, "key": key})
}

// ---- kenari keys of a client ----

var nonEnv = regexp.MustCompile(`[^A-Z0-9]+`)

func envName(client, key string) string {
	return "KENARI_KEY_" + strings.Trim(nonEnv.ReplaceAllString(strings.ToUpper(client+"_"+key), "_"), "_")
}

func (h *Handler) keys(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Client string `json:"client"`
		Action string `json:"action"` // add | remove | replace | rename
		Name   string `json:"name"`
		New    string `json:"new_name"`
		Key    string `json:"key"`
	}
	if r.Method != http.MethodPost || readJSON(r, &in) != nil {
		jerr(w, 400, "POST {client, action, name, key}")
		return
	}
	in.Name, in.Key = strings.Join(strings.Fields(in.Name), "-"), strings.TrimSpace(in.Key)
	in.New = strings.Join(strings.Fields(in.New), "-")
	next := cloneConfig(h.a.Cfg())
	var cl *config.Client
	for i := range next.Clients {
		if next.Clients[i].Name == in.Client {
			cl = &next.Clients[i]
		}
	}
	if cl == nil {
		jerr(w, 404, "no such client")
		return
	}
	switch in.Action {
	case "add":
		if in.Name == "" || in.Key == "" {
			jerr(w, 422, "a name and the key are required")
			return
		}
		env := envName(in.Client, in.Name)
		cl.Keys = append(cl.Keys, config.Upstream{Name: in.Name, KeyEnv: env})
		if err := config.Normalize(&next); err != nil {
			jerr(w, 422, err.Error())
			return
		}
		if err := config.SetSecret(next.Server.SecretsFile, env, in.Key); err != nil {
			jerr(w, 422, err.Error())
			return
		}
	case "rename":
		// the name of a key is all it has: the secret moves with it.
		if in.New == "" {
			jerr(w, 422, "a new name is required")
			return
		}
		for _, k := range cl.Keys {
			if k.Name == in.New {
				jerr(w, 422, fmt.Sprintf("this client already has a key named %q", in.New))
				return
			}
		}
		found := false
		for i := range cl.Keys {
			if cl.Keys[i].Name != in.Name {
				continue
			}
			found = true
			old := cl.Keys[i].KeyEnv
			env := envName(in.Client, in.New)
			if secret, ok := h.a.Secrets()[old]; ok {
				if err := config.SetSecret(next.Server.SecretsFile, env, secret); err != nil {
					jerr(w, 422, err.Error())
					return
				}
			}
			_ = config.DeleteSecret(next.Server.SecretsFile, old)
			cl.Keys[i].Name, cl.Keys[i].KeyEnv = in.New, env
		}
		if !found {
			jerr(w, 404, "no such key")
			return
		}
	case "replace":
		for _, k := range cl.Keys {
			if k.Name == in.Name && in.Key != "" {
				if err := h.a.SetSecret(k.KeyEnv, in.Key); err != nil {
					jerr(w, 422, err.Error())
					return
				}
				jok(w, map[string]any{"ok": true})
				return
			}
		}
		jerr(w, 404, "no such key")
		return
	case "remove":
		kept := cl.Keys[:0:0]
		for _, k := range cl.Keys {
			if k.Name == in.Name {
				_ = config.DeleteSecret(next.Server.SecretsFile, k.KeyEnv)
				continue
			}
			kept = append(kept, k)
		}
		cl.Keys = kept
	default:
		jerr(w, 400, "action must be add, remove, replace or rename")
		return
	}
	if err := h.a.Apply(&next); err != nil {
		jerr(w, 422, err.Error())
		return
	}
	jok(w, map[string]any{"ok": true})
}

// accountKey stores or removes the optional full-access key used to read plan quota and balance.
func (h *Handler) accountKey(w http.ResponseWriter, r *http.Request) {
	var in struct{ Key string }
	if r.Method != http.MethodPost || readJSON(r, &in) != nil {
		jerr(w, 400, "POST {key}")
		return
	}
	cfg := h.a.Cfg()
	next := cloneConfig(cfg)
	env := next.Kenari.AccountKeyEnv
	if env == "" {
		env = "KENARI_ACCOUNT_KEY"
		next.Kenari.AccountKeyEnv = env
	}
	if k := strings.TrimSpace(in.Key); k != "" {
		if err := config.SetSecret(next.Server.SecretsFile, env, k); err != nil {
			jerr(w, 422, err.Error())
			return
		}
	} else if err := config.DeleteSecret(next.Server.SecretsFile, env); err != nil {
		jerr(w, 422, err.Error())
		return
	}
	if err := h.a.Apply(&next); err != nil {
		jerr(w, 422, err.Error())
		return
	}
	jok(w, map[string]any{"ok": true})
}

// ---- ranking ----

func (h *Handler) ranking(w http.ResponseWriter, r *http.Request) {
	client, preset := r.URL.Query().Get("client"), r.URL.Query().Get("preset")
	rk := h.a.Rt.Rankings()
	if preset != "" {
		if x := rk[rank.Key(client, preset)]; x != nil {
			jok(w, x)
			return
		}
		jerr(w, 404, "no such preset")
		return
	}
	out := map[string]*rank.Ranking{}
	for _, v := range rk {
		if v.Client == client {
			out[v.Preset] = v
		}
	}
	jok(w, out)
}
