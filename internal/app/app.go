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
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/bench"
	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/jev"
	"github.com/axellageraldinc/xellter/internal/proxy"
	"github.com/axellageraldinc/xellter/internal/route"
	"github.com/axellageraldinc/xellter/internal/store"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// Version is set at build time.
var Version = "dev"

// App is the running system.
type App struct {
	Path    string
	Cat     *catalog.Catalog
	Pool    *upstream.Pool
	Rt      *route.Router
	St      *store.Store
	Px      *proxy.Server
	Judge   *jev.Judge
	Auth    *auth.Authenticator
	Started time.Time
	Logf    func(string, ...any)
	// BenchBase is where leaderboard data comes from; tests point it at a fake.
	BenchBase string

	cfg atomic.Pointer[config.Config]
	mu  sync.Mutex // serialises Apply
}

type dynSource struct{ a *App }

func (d dynSource) Fetch(ctx context.Context) (*catalog.Snapshot, error) {
	c := d.a.Cfg()
	k := &catalog.Kenari{BaseURL: c.Kenari.BaseURL, UA: c.Kenari.UserAgent, HTTP: &http.Client{Timeout: 30 * time.Second}}
	return k.Fetch(ctx)
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
	a := &App{Path: path, St: st, Started: time.Now(), Logf: log.Printf}
	a.cfg.Store(cfg)
	if src == nil {
		src = dynSource{a}
	}
	a.Cat = catalog.New(src, filepath.Join(cfg.Server.DataDir, "catalog.json"))
	a.Pool = upstream.NewPool()
	a.Pool.Reload(cfg, config.LoadSecrets(cfg.Server.SecretsFile), st)
	a.Rt = route.New(cfg, a.Cat, a.Pool, st)
	a.Px = proxy.New(cfg, a.Cat, a.Pool, a.Rt, st)
	a.Px.Logf = func(f string, v ...any) { a.Logf(f, v...) }
	a.Judge = &jev.Judge{Cfg: a.Cfg, Cat: a.Cat, Pool: a.Pool, St: st, HC: &http.Client{}, Rankings: a.Rt.Rankings, Logf: func(f string, v ...any) { a.Logf(f, v...) }, Now: time.Now}
	a.BenchBase = bench.DefaultBase
	a.Cat.OnUpdate(func(s *catalog.Snapshot) {
		for _, ac := range a.Pool.Accounts() {
			ac.SetTopupThreshold(s.Tier.Thresh)
		}
		a.rematchBench(s)
		a.Rt.MarkSeen(s)
		a.Rt.Recompute(time.Now())
	})
	if rec, err := auth.ReadRecord(cfg.Server.AuthRecord); err == nil {
		a.Auth = auth.NewAuthenticator(rec, filepath.Join(cfg.Server.DataDir, "login-state.json"), filepath.Join(cfg.Server.DataDir, "recovery-used.json"))
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
	if err := config.Save(a.Path, c); err != nil {
		return err
	}
	a.activate(c)
	return nil
}

func (a *App) activate(c *config.Config) {
	a.cfg.Store(c)
	a.Pool.Reload(c, config.LoadSecrets(c.Server.SecretsFile), a.St)
	a.Px.SetConfig(c)
	a.Rt.SetConfig(c)
	go a.ProbeKeys(context.Background())
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

// ProbeKeys asks kenari which models each key may use. A request for a model
// that does not exist is rejected with the key's allow list in the error and is
// never billed, so this costs nothing. Unrestricted keys clear their list.
func (a *App) ProbeKeys(ctx context.Context) {
	cfg := a.Cfg()
	hc := &http.Client{Timeout: 20 * time.Second}
	for _, k := range a.Pool.Keys() {
		if k.Secret == "" {
			continue
		}
		if k.ReadOnly() { // read-only account key: used for plan and balance, never for requests
			a.readAccount(ctx, k)
			continue
		}
		body := []byte(`{"model":"xellter-probe","max_tokens":1,"messages":[{"role":"user","content":"."}]}`)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Kenari.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+k.Secret)
		req.Header.Set("User-Agent", cfg.Kenari.UserAgent)
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
func (a *App) seedPresets() {
	snap := a.Cat.Snapshot()
	if snap == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	cur := a.Cfg()
	next := *cur
	next.Clients = make([]config.Client, len(cur.Clients))
	changed := false
	for i, cl := range cur.Clients {
		cl.Presets = clonePresets(cl.Presets)
		cl.Seeded = append([]string(nil), cl.Seeded...)
		keys := a.Pool.ClientKeys(cl.Name)
		ready := len(keys) > 0
		for _, k := range keys {
			if k.Secret != "" && !k.Probed() {
				ready = false
			}
		}
		for _, kind := range config.PresetModalities {
			if !ready || containsStr(cl.Seeded, kind) || !kindAvailable(snap, keys, kind) {
				continue
			}
			if cl.Presets == nil {
				cl.Presets = map[string]config.Preset{}
			}
			if _, exists := cl.Presets[config.PresetName(kind)]; !exists {
				cl.Presets[config.PresetName(kind)] = config.DefaultPreset(kind)
			}
			cl.Seeded = append(cl.Seeded, kind)
			changed = true
		}
		next.Clients[i] = cl
	}
	if !changed {
		return
	}
	if err := config.Normalize(&next); err != nil {
		a.Logf("preset generation: %v", err)
		return
	}
	if err := config.Save(a.Path, &next); err != nil {
		a.Logf("preset generation: %v", err)
		return
	}
	a.cfg.Store(&next)
	a.Px.SetConfig(&next)
	a.Rt.SetConfig(&next)
	a.Logf("presets generated from the models each client's keys can use")
}

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

// kindAvailable reports whether any enabled key can call a catalog model of the kind.
func kindAvailable(snap *catalog.Snapshot, keys []*upstream.Key, kind string) bool {
	for _, id := range snap.IDs() {
		m := snap.Models[id]
		if m.Endpoint != kind {
			continue
		}
		for _, k := range keys {
			if k.Secret != "" && k.AllowedModel(id) {
				return true
			}
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
	cfg := a.Cfg()
	hc := &http.Client{Timeout: 20 * time.Second}
	get := func(path string) (int, []byte) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Kenari.BaseURL+path, nil)
		req.Header.Set("Authorization", "Bearer "+k.Secret)
		req.Header.Set("User-Agent", cfg.Kenari.UserAgent)
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
	rows, err := bench.Fetch(ctx, &http.Client{Timeout: 60 * time.Second}, a.BenchBase)
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
	out := map[string]store.BenchRow{}
	for _, sc := range bench.Match(rows, snap.Models) {
		out[sc.Model] = store.BenchRow{Value: sc.Value, Rating: sc.Rating, Lo: sc.Lo, Hi: sc.Hi, Arena: sc.Arena, Matched: sc.Matched, Date: sc.Date, Votes: sc.Votes}
	}
	if err := a.St.ReplaceBench(out); err != nil {
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
	a.Rt.MarkSeen(a.Cat.Snapshot())
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
			}
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(a.Cfg().Kenari.SyncMinutes) * time.Minute):
			}
			if err := a.Cat.Refresh(ctx); err != nil {
				a.Logf("catalog refresh failed: %v", err)
			}
		}
	}()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		purge := time.NewTicker(6 * time.Hour)
		defer purge.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.Rt.Recompute(time.Now())
			case <-purge.C:
				a.St.Purge(a.Cfg().Server.LogRetention)
			}
		}
	}()
	go a.Judge.Run(ctx)
}

// Close flushes state.
func (a *App) Close() error { return a.St.Close() }

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
	return fmt.Sprintf("xellter %s: %d clients, %d kenari keys, %d presets, %d catalog models, listening on %s", Version, len(c.Clients), keys, presets, n, c.Server.Listen)
}
