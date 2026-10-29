// Package app wires the components together and owns the hot-reloadable state.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/bench"
	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/effort"
	"github.com/axellageraldinc/xellter/internal/proxy"
	"github.com/axellageraldinc/xellter/internal/route"
	"github.com/axellageraldinc/xellter/internal/store"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// Version is set at build time.
var Version = "dev"

// effortSaveInterval is how often the learned effort facts are written to the database.
//
// A restart that loses the facts costs the client a few slow answers, because the
// gateway serves an unknown model as asked until it has measured it again. A minute is
// short enough that a crash loses almost nothing and long enough that the database is
// not written to on every request.
const effortSaveInterval = time.Minute

// App is the running system.
type App struct {
	Path    string
	Cat     *catalog.Catalog
	Pool    *upstream.Pool
	Rt      *route.Router
	St      *store.Store
	Px      *proxy.Server
	Auth    *auth.Authenticator
	Started time.Time
	Logf    func(string, ...any)
	// BenchBase, SWEPage and AiderYAML are where quality data comes from; tests point them at fakes.
	BenchBase string
	SWEPage   string
	AiderYAML string

	cfg atomic.Pointer[config.Config]
	mu  sync.Mutex // serialises Apply
	// effortVersion is the registry version this process last loaded or wrote. The
	// facts are written only when the version moved, so a read-only command such as
	// `xellter explain` — which builds an app over the same data directory, loads the
	// facts and learns nothing — cannot write that older copy back over what the
	// running gateway has learned since.
	effortVersion atomic.Uint64
	// pinVersion is the router's pin version this process last loaded or wrote.
	pinVersion atomic.Uint64
	// healthVersion is the router's health version this process last loaded or wrote.
	healthVersion atomic.Uint64
	// seenDropped and seenLost are the log-loss counters last reported, so the warning is not repeated.
	seenDropped, seenLost atomic.Uint64
	// mergeSrc is the catalog source over every provider (see merge).
	mergeMu  sync.Mutex
	mergeSrc catalog.Source
	mergeKey string
	// alerted is when a client's spend alert was last sent to the webhook (in memory: a restart may send one again).
	alertMu sync.Mutex
	alerted map[string]time.Time
}

type dynSource struct{ a *App }

// Fetch builds one source per provider of the active configuration and merges them (ADR 0040). The providers are
// read on every fetch, so a provider added in the panel is listed at the next refresh.
func (d dynSource) Fetch(ctx context.Context) (*catalog.Snapshot, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	var named []catalog.Named
	for _, pc := range d.a.Cfg().ProviderList() {
		named = append(named, catalog.Named{ID: pc.ID, Src: &catalog.Kenari{BaseURL: pc.BaseURL, UA: pc.UserAgent, HTTP: hc}})
	}
	return d.a.merge(named).Fetch(ctx)
}

// merge returns the merging source for the given providers, kept between fetches so that a provider which is
// down keeps the models of its last good fetch.
func (a *App) merge(named []catalog.Named) catalog.Source {
	a.mergeMu.Lock()
	defer a.mergeMu.Unlock()
	key := ""
	for _, n := range named {
		key += n.ID + ","
	}
	if a.mergeSrc == nil || a.mergeKey != key {
		a.mergeSrc, a.mergeKey = catalog.Merge(named[0], named[1:]...), key
	}
	return a.mergeSrc
}

// locals are the local models of the active configuration, in the catalog's terms (ADR 0033).
func (a *App) locals() []catalog.Local {
	var out []catalog.Local
	for _, lm := range a.Cfg().LocalModels {
		out = append(out, catalog.Local{ID: lm.ID, Category: lm.Category, BaseURL: lm.BaseURL, Model: lm.Model})
	}
	return out
}

// New loads the configuration and builds all components (nothing runs yet).
func New(path string, src catalog.Source) (*App, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.Server.DataDir)
	if err != nil {
		return nil, err
	}
	if err := config.AttachClients(path, cfg, st); err != nil {
		_ = st.Close()
		return nil, err
	}
	a := &App{Path: path, St: st, Started: time.Now(), Logf: log.Printf, alerted: map[string]time.Time{}}
	a.cfg.Store(cfg)
	if src == nil {
		src = dynSource{a}
	}
	src = catalog.WithLocal(src, a.locals)
	importLegacyFiles(cfg.Server.DataDir, st, log.Printf)
	a.Cat = catalog.New(src, st)
	a.Pool = upstream.NewPool()
	a.Pool.Reload(cfg, config.LoadSecrets(cfg.Server.SecretsFile), st)
	a.Rt = route.New(cfg, a.Cat, a.Pool, st)
	a.Px = proxy.New(cfg, a.Cat, a.Pool, a.Rt, st)
	a.Px.Logf = func(f string, v ...any) { a.Logf(f, v...) }
	a.BenchBase = bench.DefaultBase
	a.SWEPage = bench.DefaultSWEURL
	a.AiderYAML = bench.DefaultAiderURL
	a.loadEffortFacts()
	a.loadAlerted()
	a.loadPins()
	a.loadHealth()
	a.Cat.OnUpdate(func(s *catalog.Snapshot) {
		for _, ac := range a.Pool.Accounts() {
			if ac.Name == upstream.DefaultAccount { // the snapshot's free tier is the built-in provider's
				ac.SetTopupThreshold(s.Tier.Thresh)
			}
		}
		a.rematchBench(s)
		a.Rt.Recompute(time.Now())
	})
	if rec, err := auth.ReadRecord(cfg.Server.AuthRecord); err == nil {
		a.Auth = auth.NewAuthenticator(rec, st)
	} else {
		a.Logf("admin login disabled: %v (run: xellter admin init)", err)
	}
	return a, nil
}

// Cfg returns the active configuration.
func (a *App) Cfg() *config.Config { return a.cfg.Load() }

// Secrets returns the loaded secret values.
func (a *App) Secrets() map[string]string { return config.LoadSecrets(a.Cfg().Server.SecretsFile) }

// Apply validates, persists and hot-applies a new configuration.
func (a *App) Apply(c *config.Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := config.Save(a.Path, c, a.St); err != nil {
		return err
	}
	a.activate(c)
	return nil
}

func (a *App) activate(c *config.Config) {
	old := a.cfg.Swap(c)
	if old != nil && !reflect.DeepEqual(old.LocalModels, c.LocalModels) {
		// a local model added or removed shows up now, not at the next scheduled refresh
		go func() { _ = a.Cat.Refresh(context.Background()) }()
	}
	a.Pool.Reload(c, config.LoadSecrets(c.Server.SecretsFile), a.St)
	a.Px.SetConfig(c)
	a.Rt.SetConfig(c)
	go a.ProbeKeys(context.Background())
}

// loadEffortFacts restores the refusals learned by a previous run, so a restart does not make the next request
// fail again on a model that refuses the effort field (ADR 0026, 0028).
func (a *App) loadEffortFacts() {
	if a.St == nil || a.Rt == nil {
		return
	}
	var saved []effort.Refusal
	for _, f := range a.St.EffortFacts() {
		saved = append(saved, effort.Refusal{Model: f.Model, Level: f.Level, At: f.At})
	}
	if n := a.Rt.Efforts().Restore(saved); n > 0 {
		a.Logf("effort: restored %d refusals", n)
	}
	// The version now matches what was loaded, so a process that learns nothing does not write the table back.
	a.effortVersion.Store(a.Rt.Efforts().Version())
}

// saveEffortFacts writes the refusals when there is a new one. Called from a timer and on close, never from
// the request path. A read-only command (`xellter explain`) loads the table and closes with the same version,
// so it leaves the table alone instead of writing back an older copy of what the running gateway has learned.
func (a *App) saveEffortFacts() {
	if a.St == nil || a.Rt == nil {
		return
	}
	reg := a.Rt.Efforts()
	version := reg.Version()
	if version == a.effortVersion.Load() {
		return
	}
	var facts []store.EffortFact
	for _, f := range reg.All() {
		facts = append(facts, store.EffortFact{Model: f.Model, Level: f.Level, Verdict: "rejects", At: f.At})
	}
	if err := a.St.SaveEffortFacts(facts); err != nil {
		a.Logf("effort: could not save the refusals: %v", err)
		return
	}
	a.effortVersion.Store(version)
}

// warnLogLoss says so in the log when request rows were thrown away or refused by the database since the last
// look. The same counters are on /healthz.
func (a *App) warnLogLoss() {
	if a.St == nil {
		return
	}
	dropped, lost := a.St.LogLoss()
	if dropped > a.seenDropped.Load() || lost > a.seenLost.Load() {
		a.Logf("request log: %d rows dropped (queue full) and %d lost (database refused) since start; the usage numbers are short by that much", dropped, lost)
		a.seenDropped.Store(dropped)
		a.seenLost.Store(lost)
	}
}

// healthKey is the kv row that holds the models that are out and the recent health records.
const healthKey = "model_health"

// loadHealth restores the models that were out, their strikes and the recent records, so a restart does not
// forgive a model that was slow a minute ago (ADR 0029).
func (a *App) loadHealth() {
	if a.St == nil || a.Rt == nil {
		return
	}
	out, v := a.Rt.RestoreHealth([]byte(a.St.KVGet(healthKey)), time.Now())
	a.healthVersion.Store(v)
	if out > 0 {
		a.Logf("health: %d models are still out of their presets", out)
	}
}

// saveHealth writes the health records when they changed. Called from the timer and on close.
func (a *App) saveHealth() {
	if a.St == nil || a.Rt == nil {
		return
	}
	raw, v := a.Rt.HealthState()
	if v == a.healthVersion.Load() {
		return
	}
	a.St.KVSet(healthKey, string(raw))
	a.healthVersion.Store(v)
}

// pinsKey is the kv row that holds the session pins, and pinsMaxAge how long a pin is kept for a session
// nobody came back to.
const (
	pinsKey    = "session_pins"
	pinsMaxAge = 7 * 24 * time.Hour
)

// loadPins restores the session pins of the previous run, so a restart does not move a conversation to
// another model (ADR 0028).
func (a *App) loadPins() {
	if a.St == nil || a.Rt == nil {
		return
	}
	var saved []route.PinSnapshot
	if raw := a.St.KVGet(pinsKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &saved)
	}
	n, v := a.Rt.RestorePins(saved, time.Now(), pinsMaxAge)
	a.pinVersion.Store(v)
	if n > 0 {
		a.Logf("pins: restored %d session pins", n)
	}
}

// savePins writes the session pins when they changed since the last write. Like the effort facts it is
// called from a timer and on close, never from the request path.
func (a *App) savePins() {
	if a.St == nil || a.Rt == nil {
		return
	}
	pins, v := a.Rt.PinState()
	if v == a.pinVersion.Load() {
		return
	}
	cut := time.Now().Add(-pinsMaxAge)
	kept := pins[:0]
	for _, p := range pins {
		if p.At.After(cut) {
			kept = append(kept, p)
		}
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return
	}
	a.St.KVSet(pinsKey, string(raw))
	a.pinVersion.Store(v)
}

// SetSecret stores an upstream key value and reloads the pool.
func (a *App) SetSecret(name, value string) error {
	c := a.Cfg()
	if err := config.SetSecret(c.Server.SecretsFile, name, value); err != nil {
		return err
	}
	a.Pool.Reload(c, config.LoadSecrets(c.Server.SecretsFile), a.St)
	a.Rt.Recompute(time.Now())
	go a.ProbeKeys(context.Background())
	return nil
}

// ProbeKeys asks each key's provider which models the key may use. A request for a model
// that does not exist is rejected with the key's allow list in the error and is
// never billed, so this costs nothing. Unrestricted keys clear their list.
func (a *App) ProbeKeys(ctx context.Context) {
	cfg := a.Cfg()
	hc := &http.Client{Timeout: 20 * time.Second}
	for _, k := range a.Pool.Keys() {
		if k.Secret() == "" {
			continue
		}
		if k.ReadOnly() { // read-only account key: used for plan and balance, never for requests
			a.readAccount(ctx, k)
			continue
		}
		if k.Capability() != "" {
			// A key dedicated to one service is never probed: the probe asks for a chat model, which
			// such a key may not call, and a pay-as-you-go key must not be billed for xellter's own
			// curiosity. It is used for its service only.
			continue
		}
		conn, ok := cfg.Provider(k.Provider())
		if !ok {
			continue
		}
		body := []byte(`{"model":"xellter-probe","max_tokens":1,"messages":[{"role":"user","content":"."}]}`)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, conn.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+k.Secret())
		req.Header.Set("User-Agent", conn.UserAgent)
		resp, err := hc.Do(req)
		if err != nil {
			a.Logf("probe %s: %v", k.Name, err)
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			a.Logf("probe %s: key rejected (HTTP %d)", k.Name, resp.StatusCode)
			continue
		}
		if list, ok := upstream.ParseAllowed(raw); ok {
			k.SetAllowed(list)
			a.Logf("probe %s: restricted to %d models", k.Name, len(list))
		} else if resp.StatusCode == 400 || resp.StatusCode == 404 {
			k.SetAllowed(nil)
			a.Logf("probe %s: no model restriction", k.Name)
		}
		if a.readAccount(ctx, k) {
			a.Logf("probe %s: unrestricted key, plan %q read from kenari", k.Name, k.Account().Status(a.Cat.Snapshot(), time.Now()).Plan)
		}
	}
	a.seedPresets()
	a.Rt.Recompute(time.Now())
}

// seedPresets generates the presets of every client from the models its keys can
// call: one per kind (text, image, video, tts, stt, music) that has at least one
// model. Kinds are offered once; presets the owner edits or deletes are never
// recreated or overwritten. A client is only seeded after all its keys were probed.
// Presets are not generated: the owner creates one per category in the panel and picks its models, because a
// generated preset would guess at both (ADR 0019). This is kept as a no-op so the probe loop can call it.
func (a *App) seedPresets() {}

func clonePresets(m map[string]config.Preset) map[string]config.Preset {
	out := make(map[string]config.Preset, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func containsStr(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

type quotaResp struct {
	Plan *struct {
		Name    string `json:"name"`
		Windows map[string]struct {
			BudgetRP    float64 `json:"budget_rp"`
			UsedRP      float64 `json:"used_rp"`
			RemainingRP float64 `json:"remaining_rp"`
			UsedFrac    float64 `json:"used_frac"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"windows"`
	} `json:"plan"`
}

// readAccount asks kenari for the account's plan quota and balance. Only an
// unrestricted key may; restricted keys get 403 and are simply not used for this.
func (a *App) readAccount(ctx context.Context, k *upstream.Key) bool {
	conn, ok := a.Cfg().Provider(k.Provider())
	if !ok {
		return false
	}
	hc := &http.Client{Timeout: 20 * time.Second}
	get := func(path string) (int, []byte) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, conn.BaseURL+path, nil)
		req.Header.Set("Authorization", "Bearer "+k.Secret())
		req.Header.Set("User-Agent", conn.UserAgent)
		resp, err := hc.Do(req)
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, b
	}
	acct := k.Account()
	code, raw := get("/v1/account/quota")
	if code != 200 {
		if code == 403 && acct.QuotaKey() == k.Name {
			acct.SetInfo(nil, "") // the key lost its right to read quota
		}
		return false
	}
	var q quotaResp
	if json.Unmarshal(raw, &q) != nil {
		return false
	}
	info := &upstream.PlanInfo{Fetched: time.Now(), Windows: map[string]upstream.Window{}}
	if q.Plan != nil {
		info.Name = q.Plan.Name
		for name, w := range q.Plan.Windows {
			t, _ := time.Parse(time.RFC3339, w.ResetsAt)
			info.Windows[name] = upstream.Window{BudgetRP: w.BudgetRP, UsedRP: w.UsedRP, RemainingRP: w.RemainingRP, UsedFrac: w.UsedFrac, ResetsAt: t}
		}
	}
	acct.SetInfo(info, k.Name)
	if code, raw := get("/v1/account/balance"); code == 200 {
		var b struct {
			Available float64 `json:"available_micro_idr"`
		}
		if json.Unmarshal(raw, &b) == nil {
			acct.SetBalance(b.Available/1e6, time.Now())
		}
	}
	return true
}

// syncEvery is how long to wait before the next catalog refresh: the shortest interval any provider asks for.
func (a *App) syncEvery() time.Duration {
	min := 0
	for _, pc := range a.Cfg().ProviderList() {
		if min == 0 || pc.SyncMinutes < min {
			min = pc.SyncMinutes
		}
	}
	return time.Duration(min) * time.Minute
}

// RefreshAccounts re-reads quota and balance from every account that has an unrestricted key.
func (a *App) RefreshAccounts(ctx context.Context) {
	for _, acct := range a.Pool.Accounts() {
		if name := acct.QuotaKey(); name != "" {
			if k := a.Pool.Get(name); k != nil {
				a.readAccount(ctx, k)
			}
		}
	}
	a.Rt.Recompute(time.Now())
}

// benchMaxAge is how old the stored leaderboards may get before they are downloaded again.
const benchMaxAge = 24 * time.Hour

// SyncBench downloads the public leaderboards and maps them onto the catalog.
func (a *App) SyncBench(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	rows, err := bench.Fetch(ctx, &http.Client{Timeout: 60 * time.Second}, bench.Sources{
		Base: a.BenchBase, SWE: a.SWEPage, Aider: a.AiderYAML,
	})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(rows)
	a.St.KVSet("bench_rows", string(raw))
	a.St.KVSet("bench_fetched", fmt.Sprint(time.Now().Unix()))
	a.rematchBench(a.Cat.Snapshot())
	a.Rt.Recompute(time.Now())
	return nil
}

// rematchBench maps the stored leaderboard rows onto the current catalog (cheap; no network).
func (a *App) rematchBench(snap *catalog.Snapshot) {
	if snap == nil {
		return
	}
	var rows []bench.Row
	if json.Unmarshal([]byte(a.St.KVGet("bench_rows")), &rows) != nil || len(rows) == 0 {
		return
	}
	out := map[string][]store.BenchRow{}
	for _, sc := range bench.Match(rows, snap.Models) {
		out[sc.Model] = append(out[sc.Model], store.BenchRow{Value: sc.Value, Rating: sc.Rating, Lo: sc.Lo, Hi: sc.Hi,
			Arena: sc.Arena, Matched: sc.Matched, Date: sc.Date, Votes: sc.Votes})
	}
	if err := a.St.ReplaceAllBench(out); err != nil {
		a.Logf("bench: %v", err)
	}
}

// BenchFetched is when the leaderboards were last downloaded (zero if never).
func (a *App) BenchFetched() time.Time {
	var n int64
	fmt.Sscan(a.St.KVGet("bench_fetched"), &n)
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// RefreshCatalog fetches the catalog now.
func (a *App) RefreshCatalog(ctx context.Context) error { return a.Cat.Refresh(ctx) }

// Start launches background loops and returns after the first catalog load attempt.
func (a *App) Start(ctx context.Context) {
	first, cancel := context.WithTimeout(ctx, 20*time.Second)
	if err := a.Cat.Refresh(first); err != nil {
		a.Logf("initial catalog fetch failed (using cache if present): %v", err)
	}
	cancel()
	go func() { // leaderboards: on start when stale, then re-checked every 6 hours
		for {
			if time.Since(a.BenchFetched()) > benchMaxAge {
				if err := a.SyncBench(ctx); err != nil {
					a.Logf("bench: leaderboard sync failed (price-based estimates stay in use): %v", err)
				} else {
					a.Logf("bench: leaderboards updated")
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(6 * time.Hour):
			}
		}
	}()
	a.ProbeKeys(ctx)
	go func() {
		probe := time.NewTicker(6 * time.Hour)
		quota := time.NewTicker(5 * time.Minute)
		defer probe.Stop()
		defer quota.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-probe.C:
				a.ProbeKeys(ctx)
			case <-quota.C:
				a.RefreshAccounts(ctx)
				a.NotifySpikes(ctx, time.Now())
			}
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(a.syncEvery()):
			}
			if err := a.Cat.Refresh(ctx); err != nil {
				a.Logf("catalog refresh failed: %v", err)
			}
		}
	}()
	go func() {
		// Rankings are rebuilt on the configured interval (5 minutes by default), after every catalog sync and
		// on demand from the panel. Nothing on the request path recomputes them. A zero interval means "only on
		// a sync or a Recalculate", so no timer is started at all.
		var refresh <-chan time.Time
		if m := a.Cfg().Routing.RankRefreshMinutes; m > 0 {
			t := time.NewTicker(time.Duration(m) * time.Minute)
			defer t.Stop()
			refresh = t.C
		}
		purge := time.NewTicker(6 * time.Hour)
		defer purge.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-refresh:
				a.Rt.Recompute(time.Now())
			case <-purge.C:
				a.St.Purge(a.Cfg().Server.LogRetention)
			}
		}
	}()
	go func() {
		// What the gateway learned about models is saved on a timer, so a restart does
		// not have to learn it again. The timer is short enough that a crash loses
		// little, and long enough that the database is not written to constantly.
		//
		// The facts are also written when the service stops, so a planned restart does
		// not lose the last few minutes of learning.
		t := time.NewTicker(effortSaveInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				a.saveEffortFacts()
				a.savePins()
				a.saveHealth()
				return
			case <-t.C:
				a.saveEffortFacts()
				a.savePins()
				a.saveHealth()
				a.warnLogLoss()
			}
		}
	}()
}

// Close flushes state.
func (a *App) Close() error {
	// The learned facts are written here as well as on the timer, so a planned
	// restart does not lose what the gateway learned since the last tick.
	a.saveEffortFacts()
	a.savePins()
	a.saveHealth()
	return a.St.Close()
}

// Describe is a one-line startup summary.
func (a *App) Describe() string {
	c := a.Cfg()
	n := 0
	if s := a.Cat.Snapshot(); s != nil {
		n = len(s.Models)
	}
	keys, presets := 0, 0
	for _, cl := range c.Clients {
		keys += len(cl.Keys)
		presets += len(cl.Presets)
	}
	return fmt.Sprintf("xellter %s: %d clients, %d upstream keys, %d presets, %d catalog models, listening on %s", Version, len(c.Clients), keys, presets, n, c.Server.Listen)
}
