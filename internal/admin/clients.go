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

// ---- labels ----
//
// The panel renders these instead of the stored values, so a purpose reads
// "Vision & documents" while the config keeps the stable "vision".

var purposeLabel = map[string]string{
	"general": "General", "coding": "Coding", "agentic": "Agentic",
	"vision": "Vision & documents", "bulk": "Bulk",
}

var optimizationLabel = map[string]string{
	"cheapest": "Cheapest", "balanced": "Balanced", "best_quality": "Best Quality",
}

var categoryLabel = map[string]string{
	"chat": "Text", "images": "Image", "videos": "Video", "audio_speech": "Text to speech",
	"audio_transcription": "Speech to text", "music": "Music", "systemone": "System One",
}

type option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func purposeOptions() []option {
	out := make([]option, 0, len(config.Purposes))
	for _, p := range config.Purposes {
		out = append(out, option{p, purposeLabel[p]})
	}
	return out
}

func optimizationOptions() []option {
	out := make([]option, 0, len(config.Optimizations))
	for _, o := range config.Optimizations {
		out = append(out, option{o, optimizationLabel[o]})
	}
	return out
}

// categoryOption is one preset a client may hold: the category and how the
// panel names it. The preset itself is named by the owner (ADR 0021), so the
// suggested name is only a starting point.
type categoryOption struct {
	Category string `json:"category"`
	Preset   string `json:"preset"`
	Label    string `json:"label"`
}

func categoryOptions() []categoryOption {
	out := make([]categoryOption, 0, len(config.Categories))
	for _, c := range config.Categories {
		out = append(out, categoryOption{Category: c, Preset: config.PresetName(c), Label: categoryLabel[c]})
	}
	return out
}

// ---- rows the panel renders ----

type alert struct {
	Level  string `json:"level"` // info | warn | bad
	Text   string `json:"text"`
	Client string `json:"client,omitempty"` // client page to open
}

// keyRow is one kenari key of a client. The value is never here: only whether
// it exists, how far its probe got, and what it learned.
type keyRow struct {
	Name         string    `json:"name"`
	Hint         string    `json:"hint"` // the last four characters, so keys can be told apart; never the key
	HasSecret    bool      `json:"has_secret"`
	Probed       bool      `json:"probed"`
	Unrestricted bool      `json:"unrestricted"` // may call every model
	Allowed      int       `json:"allowed"`      // models in the learned allow list (0 + unrestricted = all)
	Blocked      int       `json:"blocked"`      // models kenari refused on this key right now
	CooldownEnd  time.Time `json:"cooldown_until"`
	BreakerEnd   time.Time `json:"breaker_until"`
	LastError    string    `json:"last_error"`
	LastOK       time.Time `json:"last_ok"`
	Tier         string    `json:"tier"`
	State        string    `json:"state"` // ready | paused | no secret | unchecked
}

// presetRow is the summary of one preset a client holds.
type presetRow struct {
	Name              string `json:"name"`
	Category          string `json:"category"`
	Label             string `json:"label"`
	Optimization      string `json:"optimization"`
	OptimizationLabel string `json:"optimization_label"`
	Pool              int    `json:"pool"`    // models the owner picked
	Serving           int    `json:"serving"` // models in the ranked list
	Top               string `json:"top"`     // the model that goes first
	Rejected          int    `json:"rejected"`
	Unranked          int    `json:"unranked"`
}

// clientRow is one client in the list.
type clientRow struct {
	Name         string      `json:"name"`
	Enabled      bool        `json:"enabled"`
	Purpose      string      `json:"purpose"`
	PurposeLabel string      `json:"purpose_label"`
	KeyHint      string      `json:"key_hint"` // hint of the client's xellter key, never the key
	Note         string      `json:"note"`
	Keys         []keyRow    `json:"keys"`
	Presets      []presetRow `json:"presets"`
	Requests     int         `json:"requests_24h"`
	Errors       int         `json:"errors_24h"`
	Cost         float64     `json:"cost_24h"`
	RTKSaved     int64       `json:"rtk_saved_24h"`
}

// ---- overview ----

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	snap := h.a.Cat.Snapshot()
	now := time.Now()
	cfg := h.a.Cfg()
	out := map[string]any{
		"uptime_s":      int(now.Sub(h.a.Started).Seconds()),
		"computed_at":   h.a.Rt.Computed(),
		"routing":       cfg.Routing,
		"purposes":      purposeOptions(),
		"optimizations": optimizationOptions(),
		"categories":    categoryOptions(),
		"requests": map[string]any{
			"total": h.a.Px.Total.Load(), "failed": h.a.Px.Failed.Load(),
			"inflight": h.a.Px.Inflight.Load(), "rtk_saved_chars": h.a.Px.RTKSaved.Load(),
		},
	}
	var accts []upstream.AccountStatus
	var accountKey *keyRow
	if snap != nil {
		out["catalog"] = map[string]any{"models": len(snap.Models), "age_s": int(now.Sub(snap.Fetched).Seconds()), "degraded": snap.Degraded, "plans": len(snap.Plans), "tier": snap.Tier, "usd_idr": snap.USDIDR}
		for _, k := range h.a.Pool.Keys() {
			if k.Owner() == "" { // the read-only account key
				row := h.keyRow(k, now)
				accountKey = &row
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
	rows := h.clientRows(cfg, now, 24*time.Hour)
	out["clients"] = rows
	out["alerts"], out["status"] = h.alerts(snap, rows, accts, now)
	jok(w, out)
}

// clientRows builds one row per configured client, with its keys and presets
// and its traffic over the window.
func (h *Handler) clientRows(cfg *config.Config, now time.Time, window time.Duration) []clientRow {
	usage := map[string]clientRow{}
	if rows, err := h.a.St.Usage("client", now.Add(-window), "", ""); err == nil {
		for _, u := range rows {
			usage[u.Key] = clientRow{Requests: u.Requests, Errors: u.Errors, Cost: u.Cost, RTKSaved: u.RTKSaved}
		}
	}
	rows := make([]clientRow, 0, len(cfg.Clients))
	for i := range cfg.Clients {
		cl := &cfg.Clients[i]
		u := usage[cl.Name]
		row := clientRow{
			Name: cl.Name, Enabled: cl.Enabled, Purpose: cl.Purpose, PurposeLabel: purposeLabel[cl.Purpose],
			KeyHint: cl.KeyHint, Note: cl.Note, Keys: h.keyRows(cl, now), Presets: h.presetRows(cl),
			Requests: u.Requests, Errors: u.Errors, Cost: u.Cost, RTKSaved: u.RTKSaved,
		}
		rows = append(rows, row)
	}
	return rows
}

// keyRow renders one key: whether its secret exists, what the probe learned and
// whether kenari is currently pausing it.
func (h *Handler) keyRow(k *upstream.Key, now time.Time) keyRow {
	st := k.Status(now)
	row := keyRow{
		Name: st.Label, Hint: keyHint(k), HasSecret: st.HasSecret, Probed: k.Probed(), Unrestricted: st.Unrestricted,
		Allowed: len(st.Learned), Blocked: len(st.Blocked), CooldownEnd: st.CooldownEnd, BreakerEnd: st.BreakerEnd,
		LastError: st.LastError, LastOK: st.LastOK, Tier: st.Tier,
	}
	switch {
	case !row.HasSecret:
		row.State = "no secret"
	case now.Before(row.CooldownEnd) || now.Before(row.BreakerEnd):
		row.State = "paused"
	case !row.Probed:
		row.State = "unchecked"
	default:
		row.State = "ready"
	}
	return row
}

// keyHint is the last four characters of a key, which is enough to tell two
// keys apart and useless to anyone who reads it.
func keyHint(k *upstream.Key) string {
	s := k.Secret()
	if len(s) < 4 {
		return ""
	}
	return "…" + s[len(s)-4:]
}

func (h *Handler) keyRows(cl *config.Client, now time.Time) []keyRow {
	byName := map[string]*upstream.Key{}
	for _, k := range h.a.Pool.ClientKeys(cl.Name) {
		byName[k.Label()] = k
	}
	out := make([]keyRow, 0, len(cl.Keys))
	for _, u := range cl.Keys {
		if k := byName[u.Name]; k != nil {
			out = append(out, h.keyRow(k, now))
			continue
		}
		out = append(out, keyRow{Name: u.Name, State: "no secret"})
	}
	return out
}

// presetRows summarizes every preset of a client from the live rankings.
func (h *Handler) presetRows(cl *config.Client) []presetRow {
	rk := h.a.Rt.Rankings()
	names := make([]string, 0, len(cl.Presets))
	for n := range cl.Presets {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return presetOrder(cl.Presets[names[i]].Category, names[i]) < presetOrder(cl.Presets[names[j]].Category, names[j])
	})
	rows := make([]presetRow, 0, len(names))
	for _, n := range names {
		p := cl.Presets[n]
		row := presetRow{
			Name: n, Category: p.Category, Label: categoryLabel[p.Category],
			Optimization: p.Optimization, OptimizationLabel: optimizationLabel[p.Optimization], Pool: len(p.Models),
		}
		if x := rk[rank.Key(cl.Name, n)]; x != nil {
			row.Serving, row.Rejected, row.Unranked = len(x.Candidates), len(x.Rejected), len(x.Unranked)
			if len(x.Candidates) > 0 {
				row.Top = x.Candidates[0].Model
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// presetOrder sorts presets by category, in the display order of the plan, and
// by name inside a category. The preset name is the owner's own (ADR 0021), so
// the category is what decides the order, not the name.
func presetOrder(category, name string) string {
	for i, c := range config.Categories {
		if c == category {
			return fmt.Sprintf("%02d", i)
		}
	}
	return "99" + name
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
		if len(c.Keys) == 0 {
			add("info", fmt.Sprintf("%s has no kenari key yet, so it cannot serve requests.", c.Name), c.Name)
		}
		for _, k := range c.Keys {
			switch {
			case !k.HasSecret:
				add("bad", fmt.Sprintf("%s: key %q has no key value.", c.Name, k.Name), c.Name)
			case k.State == "paused":
				add("warn", fmt.Sprintf("%s: key %q is paused after errors%s.", c.Name, k.Name, lastErr(k.LastError)), c.Name)
			}
		}
		for _, p := range c.Presets {
			switch {
			case p.Pool == 0:
				add("info", fmt.Sprintf("%s: preset %q has no models picked yet.", c.Name, p.Name), c.Name)
			case p.Top == "" && len(c.Keys) > 0:
				add("warn", fmt.Sprintf("%s: preset %q has no usable model right now.", c.Name, p.Name), c.Name)
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
		next.Clients[i] = cl
	}
	return next
}

func clonePresets(m map[string]config.Preset) map[string]config.Preset {
	out := make(map[string]config.Preset, len(m))
	for k, v := range m {
		v.Models = append([]string{}, v.Models...)
		out[k] = v
	}
	return out
}

func findClient(cfg *config.Config, name string) *config.Client {
	for i := range cfg.Clients {
		if cfg.Clients[i].Name == name {
			return &cfg.Clients[i]
		}
	}
	return nil
}

// clients lists the clients on a GET, and creates, updates, rotates or deletes
// one on a POST.
func (h *Handler) clients(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg := h.a.Cfg()
		now := time.Now()
		jok(w, map[string]any{
			"clients":       h.clientRows(cfg, now, 24*time.Hour),
			"purposes":      purposeOptions(),
			"optimizations": optimizationOptions(),
		})
		return
	}
	var in struct {
		Action   string `json:"action"` // create | update | rotate | delete
		Name     string `json:"name"`
		Purpose  string `json:"purpose"`
		Enabled  *bool  `json:"enabled"`
		Note     string `json:"note"`
		Key      string `json:"key"` // optional kenari key on create
		KeyName  string `json:"key_name"`
		KeepNote bool   `json:"keep_note"`
	}
	if r.Method != http.MethodPost || readJSON(r, &in) != nil {
		jerr(w, 400, "POST {action, name, purpose, key}")
		return
	}
	in.Name = strings.Join(strings.Fields(in.Name), "-")
	next := cloneConfig(h.a.Cfg())
	key := newKey()
	hash, hint := proxy.HashKey(key), "…"+key[len(key)-4:]
	var secretEnv, secretVal string
	switch in.Action {
	case "create":
		if in.Name == "" {
			jerr(w, 422, "a name is required")
			return
		}
		purpose := in.Purpose
		if purpose == "" {
			purpose = "general"
		}
		cl := config.Client{Name: in.Name, Enabled: true, Purpose: purpose, KeyHash: hash, KeyHint: hint, Note: in.Note, Presets: map[string]config.Preset{}}
		if k := strings.TrimSpace(in.Key); k != "" {
			name := in.KeyName
			if name == "" {
				name = "main"
			}
			name = strings.Join(strings.Fields(name), "-")
			secretEnv, secretVal = envName(in.Name, name), k
			cl.Keys = append(cl.Keys, config.Upstream{Name: name, KeyEnv: secretEnv})
		}
		next.Clients = append(next.Clients, cl)
	case "update":
		cl := findClient(&next, in.Name)
		if cl == nil {
			jerr(w, 404, "no such client")
			return
		}
		if in.Purpose != "" {
			cl.Purpose = in.Purpose
		}
		if in.Enabled != nil {
			cl.Enabled = *in.Enabled
		}
		if !in.KeepNote {
			cl.Note = in.Note
		}
		key = ""
	case "rotate":
		cl := findClient(&next, in.Name)
		if cl == nil {
			jerr(w, 404, "no such client")
			return
		}
		cl.KeyHash, cl.KeyHint = hash, hint
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
		jerr(w, 400, "action must be create, update, rotate or delete")
		return
	}
	if err := config.Normalize(&next); err != nil {
		jerr(w, 422, err.Error())
		return
	}
	// The secret is written before the config that references it, so a key in
	// the config always has a value behind it.
	if secretEnv != "" {
		if err := config.SetSecret(next.Server.SecretsFile, secretEnv, secretVal); err != nil {
			jerr(w, 422, err.Error())
			return
		}
	}
	if err := h.a.Apply(&next); err != nil {
		if secretEnv != "" {
			_ = config.DeleteSecret(next.Server.SecretsFile, secretEnv)
		}
		jerr(w, 422, err.Error())
		return
	}
	jok(w, map[string]any{"ok": true, "key": key, "computed_at": h.a.Rt.Computed()})
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
	cl := findClient(&next, in.Client)
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
				jok(w, map[string]any{"ok": true, "computed_at": h.a.Rt.Computed()})
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
	jok(w, map[string]any{"ok": true, "computed_at": h.a.Rt.Computed()})
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

// ---- one client: the whole page in one payload ----

// clientView is the stored client without anything secret: no key hash, no key values.
type clientView struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Purpose string `json:"purpose"`
	KeyHint string `json:"key_hint"`
	Note    string `json:"note"`
}

// presetView is one preset with its live ranking and its eviction state.
type presetView struct {
	Name              string               `json:"name"`
	Category          string               `json:"category"`
	Label             string               `json:"label"`
	Optimization      string               `json:"optimization"`
	OptimizationLabel string               `json:"optimization_label"`
	Models            []string             `json:"models"`
	AutoModels        []string             `json:"auto_models"` // free and promo models xellter added on its own
	Ranking           *rank.Ranking        `json:"ranking"`
	Evictions         map[string]time.Time `json:"evictions"`
}

func (h *Handler) clientDetail(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	cfg := h.a.Cfg()
	cl := findClient(cfg, name)
	if cl == nil {
		jerr(w, 404, "no such client")
		return
	}
	now := time.Now()
	rows := h.clientRows(cfg, now, 24*time.Hour)
	var row clientRow
	for _, x := range rows {
		if x.Name == name {
			row = x
		}
	}
	rk := h.a.Rt.Rankings()
	names := make([]string, 0, len(cl.Presets))
	for n := range cl.Presets {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return presetOrder(cl.Presets[names[i]].Category, names[i]) < presetOrder(cl.Presets[names[j]].Category, names[j])
	})
	presets := make([]presetView, 0, len(names))
	for _, n := range names {
		p := cl.Presets[n]
		own := map[string]bool{}
		for _, id := range p.Models {
			own[id] = true
		}
		auto := []string{}
		for _, id := range rank.AutoPool(cfg, h.a.Cat.Snapshot(), h.a.Pool, cl, p.Category, now) {
			if !own[id] {
				auto = append(auto, id)
			}
		}
		presets = append(presets, presetView{
			Name: n, Category: p.Category, Label: categoryLabel[p.Category],
			Optimization: p.Optimization, OptimizationLabel: optimizationLabel[p.Optimization],
			Models: append([]string{}, p.Models...), AutoModels: auto, Ranking: rk[rank.Key(cl.Name, n)],
			Evictions: h.a.Rt.Evictions(cl.Name, n, now),
		})
	}
	jok(w, map[string]any{
		"client":        clientView{Name: cl.Name, Enabled: cl.Enabled, Purpose: cl.Purpose, KeyHint: cl.KeyHint, Note: cl.Note},
		"row":           row,
		"keys":          h.keyRows(cl, now),
		"presets":       presets,
		"purposes":      purposeOptions(),
		"optimizations": optimizationOptions(),
		"categories":    categoryOptions(),
		"computed_at":   h.a.Rt.Computed(),
		"routing":       cfg.Routing,
	})
}

// ---- presets ----

func (h *Handler) presets(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Client       string   `json:"client"`
		Action       string   `json:"action"` // save | delete | rename
		Preset       string   `json:"preset"`
		NewName      string   `json:"new_name"`
		Category     string   `json:"category"`
		Optimization string   `json:"optimization"`
		Models       []string `json:"models"`
	}
	if r.Method != http.MethodPost || readJSON(r, &in) != nil {
		jerr(w, 400, "POST {client, action, preset, category, optimization, models}")
		return
	}
	next := cloneConfig(h.a.Cfg())
	cl := findClient(&next, in.Client)
	if cl == nil {
		jerr(w, 404, "no such client")
		return
	}
	name := strings.TrimSpace(in.Preset)
	if name == "" {
		jerr(w, 422, "send a preset name")
		return
	}
	switch in.Action {
	case "save":
		category := in.Category
		if category == "" {
			// A preset written before presets could be renamed carries its category in its name.
			category = config.CategoryOfPreset(name)
		}
		if category == "" {
			jerr(w, 422, fmt.Sprintf("%q has no category; send one of %v", name, config.Categories))
			return
		}
		opt := in.Optimization
		if opt == "" {
			opt = "balanced"
		}
		models := make([]string, 0, len(in.Models))
		seen := map[string]bool{}
		for _, m := range in.Models {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			models = append(models, m)
		}
		cl.Presets[name] = config.Preset{Category: category, Optimization: opt, Models: models}
	case "rename":
		newName := strings.TrimSpace(in.NewName)
		if newName == "" {
			jerr(w, 422, "send the new preset name")
			return
		}
		old, ok := cl.Presets[name]
		if !ok {
			jerr(w, 404, "no such preset")
			return
		}
		if _, taken := cl.Presets[newName]; taken {
			jerr(w, 409, fmt.Sprintf("this client already has a preset named %q", newName))
			return
		}
		// The preset keeps its category, its optimization and its pool: only the name the
		// client asks for changes (ADR 0021).
		delete(cl.Presets, name)
		cl.Presets[newName] = old
	case "delete":
		if _, ok := cl.Presets[name]; !ok {
			jerr(w, 404, "no such preset")
			return
		}
		delete(cl.Presets, name)
	default:
		jerr(w, 400, "action must be save, rename or delete")
		return
	}
	if err := h.a.Apply(&next); err != nil {
		jerr(w, 422, err.Error())
		return
	}
	jok(w, map[string]any{"ok": true, "computed_at": h.a.Rt.Computed()})
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
	jok(w, map[string]any{"computed_at": h.a.Rt.Computed(), "presets": out})
}

// ---- the model picker's catalog ----

// catalogRow is one model as the picker shows it: the exact fields requirement 9
// asks for, in the client's own billing terms.
type catalogRow struct {
	ID            string         `json:"id"`
	Owner         string         `json:"owner"` // vendor family
	Endpoint      string         `json:"endpoint"`
	Context       int            `json:"context"`
	Modalities    string         `json:"modalities"`
	ToolCall      bool           `json:"tool_call"`
	Free          bool           `json:"free"`
	Billing       string         `json:"billing"` // free | plan | balance
	Callable      bool           `json:"callable"`
	InIDR         float64        `json:"in_idr"`
	OutIDR        float64        `json:"out_idr"`
	CacheReadIDR  float64        `json:"cache_read_idr"`
	CacheWriteIDR float64        `json:"cache_write_idr"`
	HasCacheWrite bool           `json:"has_cache_write"`
	Lines         []catalog.Line `json:"lines,omitempty"`
	RefCostIDR    float64        `json:"ref_cost_idr"`
	RefCostKnown  bool           `json:"ref_cost_known"`
	PromoOff      float64        `json:"promo_off"`
	PromoEnds     *time.Time     `json:"promo_ends,omitempty"`
	SunsetAt      *time.Time     `json:"sunset_at,omitempty"`
	Subscriber    bool           `json:"subscriber_only"`
}

func (h *Handler) catalogModels(w http.ResponseWriter, r *http.Request) {
	snap := h.a.Cat.Snapshot()
	now := time.Now()
	q := r.URL.Query()
	category := q.Get("category")
	if category == "" {
		category = q.Get("modality") // the old name, still understood
	}
	var cl *config.Client
	if name := q.Get("client"); name != "" {
		cl = findClient(h.a.Cfg(), name)
	}
	out := map[string]any{"category": category, "client": q.Get("client"), "models": []catalogRow{}}
	if snap == nil {
		jok(w, out)
		return
	}
	rows := make([]catalogRow, 0, len(snap.Models))
	for _, id := range snap.IDs() {
		m := snap.Models[id]
		if category != "" && !catalogCategory(m, category) {
			continue
		}
		row := catalogRow{
			ID: id, Owner: m.Owner, Endpoint: m.Endpoint, Context: m.Context, Modalities: modalitiesOf(m),
			ToolCall: m.ToolCall, Free: m.Free, Billing: rank.BillingBalance, Callable: cl == nil,
			InIDR: m.InIDR, OutIDR: m.OutIDR, CacheReadIDR: m.CachedIDR, CacheWriteIDR: m.CacheWrite,
			HasCacheWrite: m.CacheWrite > 0, Lines: m.Lines, SunsetAt: m.SunsetAt, Subscriber: m.Subscriber,
		}
		if cl != nil {
			row.Billing, row.Callable = h.billingOf(cl, m, snap, now)
		} else if m.Free {
			row.Billing = rank.BillingFree
		}
		if m.PromoActive(now, 0) {
			row.PromoOff, row.PromoEnds = m.Promo.PercentOff, &m.Promo.EndsAt
		}
		row.RefCostIDR, row.RefCostKnown = rank.RefCost(m, categoryOrEndpoint(category, m), now)
		rows = append(rows, row)
	}
	out["models"] = rows
	jok(w, out)
}

// catalogCategory reports whether a model can serve a preset category.
func catalogCategory(m *catalog.Model, category string) bool {
	return m.Endpoint == category
}

// categoryOrEndpoint falls back to the model's own endpoint when no category
// was asked for, so the reference price is always the right profile.
func categoryOrEndpoint(category string, m *catalog.Model) string {
	if category != "" {
		return category
	}
	return m.Endpoint
}

// billingOf reports how this client pays for the model, and whether any of its
// keys may call it at all.
func (h *Handler) billingOf(cl *config.Client, m *catalog.Model, snap *catalog.Snapshot, now time.Time) (string, bool) {
	usable, plan := false, false
	for _, k := range h.a.Pool.ClientKeys(cl.Name) {
		if k.Secret() == "" || !k.AllowedModel(m.ID) {
			continue
		}
		usable = true
		if !m.Free && k.Covered(m, snap, now) {
			plan = true
		}
	}
	switch {
	case !usable:
		return rank.BillingBalance, false
	case m.Free:
		return rank.BillingFree, true
	case plan:
		return rank.BillingPlan, true
	}
	return rank.BillingBalance, true
}

// modalitiesOf is the short modality line the picker shows ("text+image").
func modalitiesOf(m *catalog.Model) string {
	if m.Endpoint != "chat" && m.Endpoint != "systemone" {
		return m.Endpoint
	}
	if m.Vision {
		return "text+image"
	}
	return "text"
}
