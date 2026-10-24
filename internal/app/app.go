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
	a.Cat.OnUpdate(func(s *catalog.Snapshot) {
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
		if !k.Config().Enabled || k.Secret == "" {
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
	a.Rt.Recompute(time.Now())
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
	return fmt.Sprintf("xellter %s: %d upstreams, %d clients, %d presets, %d catalog models, listening on %s", Version, len(c.Upstreams), len(c.Clients), len(c.Presets), n, c.Server.Listen)
}
