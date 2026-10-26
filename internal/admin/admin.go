// Package admin serves the dashboard UI and its JSON API under /admin.
//
// Access rules: the Host header must be in server.admin_hosts (so a public
// hostname that only fronts /v1 never exposes the panel), every API call needs
// a session cookie, and state-changing calls need the X-Xellter header
// (cookie is SameSite=Strict as well).
package admin

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/bench"
	"github.com/axellageraldinc/xellter/internal/config"
)

//go:embed ui
var uiFS embed.FS

const cookieName = "xl_session"

// Handler is the admin HTTP handler (mount it at /admin/).
type Handler struct {
	a   *app.App
	mux *http.ServeMux
	ui  http.Handler
}

// New builds the admin handler.
func New(a *app.App) *Handler {
	sub, _ := fs.Sub(uiFS, "ui")
	h := &Handler{a: a, mux: http.NewServeMux(), ui: http.StripPrefix("/admin/", http.FileServer(http.FS(sub)))}
	api := func(p string, f http.HandlerFunc) { h.mux.HandleFunc("/admin/api/"+p, h.guard(f)) }
	h.mux.HandleFunc("/admin/api/login", h.login)
	api("logout", h.logout)
	api("logout-all", h.logoutAll)
	api("me", h.me)
	api("overview", h.overview)
	api("config", h.config)
	api("rollback", h.rollback)
	api("clients", h.clients)
	api("client", h.clientDetail)
	api("presets", h.presets)
	api("keys", h.keys)
	api("account-key", h.accountKey)
	api("keys/reset", h.resetKey)
	api("keys/probe", h.probeKeys)
	api("ranking", h.ranking)
	api("recompute", h.recompute)
	api("catalog/models", h.catalogModels)
	api("catalog/refresh", h.refreshCatalog)
	api("usage", h.usage)
	api("usage/drill", h.usageDrill)
	api("usage/keys", h.usageKeys)
	api("session", h.sessionInfo)
	api("requests", h.requests)
	api("bench", h.benchInfo)
	api("bench/refresh", h.benchRefresh)
	h.mux.HandleFunc("/admin/", h.static)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		host = hh
	}
	if !config.MatchAny(h.a.Cfg().Server.AdminHosts, strings.ToLower(host)) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) static(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	h.ui.ServeHTTP(w, r)
}

func (h *Handler) clientIP(r *http.Request) string {
	if h.a.Cfg().Server.TrustedProxy {
		if x := r.Header.Get("X-Forwarded-For"); x != "" {
			return strings.TrimSpace(strings.Split(x, ",")[0])
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func (h *Handler) secure(r *http.Request) bool {
	return r.TLS != nil || (h.a.Cfg().Server.TrustedProxy && r.Header.Get("X-Forwarded-Proto") == "https")
}

func (h *Handler) guard(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || !h.a.St.SessionValid(auth.HashToken(c.Value)) {
			jerr(w, 401, "login required")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Xellter") == "" {
			jerr(w, 403, "missing X-Xellter header")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		f(w, r)
	}
}

func jerr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func jok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// ---- session ----

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("X-Xellter") == "" {
		jerr(w, 405, "POST with X-Xellter header required")
		return
	}
	if h.a.Auth == nil {
		jerr(w, 503, "admin login is not set up; run `xellter admin init` on the server")
		return
	}
	var in struct{ Username, Password, Code string }
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "bad request")
		return
	}
	res := h.a.Auth.Verify(in.Username, in.Password, in.Code, h.clientIP(r))
	if res.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(res.RetryAfter.Seconds())+1))
		jerr(w, 429, "too many attempts; try again later")
		return
	}
	if !res.OK {
		jerr(w, 401, "invalid credentials or code")
		return
	}
	tok, hash := auth.NewToken()
	ttl := time.Duration(h.a.Cfg().Server.SessionHours) * time.Hour
	h.a.St.PutSession(hash, time.Now().Add(ttl), h.clientIP(r), r.UserAgent())
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/admin", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: h.secure(r), MaxAge: int(ttl.Seconds())})
	jok(w, map[string]any{"ok": true})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		h.a.St.DeleteSession(auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/admin", MaxAge: -1})
	jok(w, map[string]any{"ok": true})
}

func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	h.a.St.DeleteAllSessions()
	jok(w, map[string]any{"ok": true})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	jok(w, map[string]any{"user": h.a.Auth.Username(), "version": app.Version, "sessions": h.a.St.SessionCount()})
}

// ---- overview ----

// ---- upstream keys ----

// ---- config ----

func (h *Handler) config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c := h.a.Cfg()
		y, _ := yaml.Marshal(c)
		jok(w, map[string]any{"config": c, "yaml": string(y), "history": config.History(c.Server.DataDir)})
	case http.MethodPut:
		var in struct {
			Config *config.Config  `json:"config"`
			YAML   *string         `json:"yaml"`
			Raw    json.RawMessage `json:"-"`
		}
		if err := readJSON(r, &in); err != nil {
			jerr(w, 400, "bad JSON: "+err.Error())
			return
		}
		var next *config.Config
		switch {
		case in.YAML != nil && in.Config != nil:
			// A GET /config response carries both. Sending it back unchanged would
			// apply the old yaml and silently drop the patched config, so refuse.
			jerr(w, 400, "send {config} or {yaml}, not both")
			return
		case in.YAML != nil:
			c, err := config.Parse([]byte(*in.YAML))
			if err != nil {
				jerr(w, 422, err.Error())
				return
			}
			next = c
			keepPaths(next, h.a.Cfg())
		case in.Config != nil:
			next = in.Config
			if err := config.Normalize(next); err != nil {
				jerr(w, 422, err.Error())
				return
			}
		default:
			jerr(w, 400, "send {config} or {yaml}")
			return
		}
		if err := h.a.Apply(next); err != nil {
			jerr(w, 422, err.Error())
			return
		}
		jok(w, map[string]any{"ok": true})
	default:
		jerr(w, 405, "GET or PUT")
	}
}

// keepPaths keeps the resolved absolute paths when a YAML edit leaves them relative.
func keepPaths(next, cur *config.Config) {
	s := &next.Server
	if s.DataDir == "" || s.DataDir == "data" || !strings.HasPrefix(s.DataDir, "/") {
		s.DataDir = cur.Server.DataDir
	}
	if s.SecretsFile == "" || !strings.HasPrefix(s.SecretsFile, "/") {
		s.SecretsFile = cur.Server.SecretsFile
	}
	if s.AuthRecord == "" || !strings.HasPrefix(s.AuthRecord, "/") {
		s.AuthRecord = cur.Server.AuthRecord
	}
}

func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID string }
	if r.Method != http.MethodPost || readJSON(r, &in) != nil {
		jerr(w, 400, "POST {id}")
		return
	}
	b, err := config.ReadHistory(h.a.Cfg().Server.DataDir, in.ID)
	if err != nil {
		jerr(w, 404, "no such version")
		return
	}
	c, err := config.Parse(b)
	if err != nil {
		jerr(w, 422, err.Error())
		return
	}
	keepPaths(c, h.a.Cfg())
	if err := h.a.Apply(c); err != nil {
		jerr(w, 422, err.Error())
		return
	}
	jok(w, map[string]any{"ok": true})
}

// ---- clients ----

func (h *Handler) resetKey(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if readJSON(r, &in) != nil {
		jerr(w, 400, "POST {name}")
		return
	}
	k := h.a.Pool.Get(in.Name)
	if k == nil {
		jerr(w, 404, "no such upstream")
		return
	}
	k.ResetState()
	h.a.Rt.Recompute(time.Now())
	jok(w, map[string]any{"ok": true})
}

func (h *Handler) probeKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	h.a.ProbeKeys(ctx)
	jok(w, map[string]any{"ok": true})
}

// ---- ranking / catalog ----

// recompute is the panel's Recalculate button. With ?client= it recomputes every preset of that client; with
// no client it recomputes everything. Rankings are a pure function of the inputs, so this can never make the
// order worse, only current.
func (h *Handler) recompute(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	h.a.Rt.Recompute(start)
	client := r.URL.Query().Get("client")
	out := map[string]any{"ok": true, "computed": h.a.Rt.Computed()}
	if client != "" {
		// The client is checked against the configuration, not the rankings: a
		// client with no catalog yet is still a client, and its Recalculate must
		// not report it as missing.
		if findClient(h.a.Cfg(), client) == nil {
			jerr(w, 404, "no such client")
			return
		}
		var presets []string
		for key, rk := range h.a.Rt.Rankings() {
			if rk.Client == client {
				presets = append(presets, key)
			}
		}
		sort.Strings(presets)
		out["client"], out["presets"] = client, presets
	}
	jok(w, out)
}

func (h *Handler) refreshCatalog(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := h.a.RefreshCatalog(ctx); err != nil {
		jerr(w, 502, err.Error())
		return
	}
	jok(w, map[string]any{"ok": true})
}

// ---- usage ----

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*90 {
		hours = 24
	}
	g := r.URL.Query().Get("group")
	if g == "" {
		g = "model"
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	// Only whole buckets count: the first bucket must lie fully inside the
	// window, so a half hour at the left edge never looks like a full one.
	switch g {
	case "day":
		since = ceilTo(since, 24*time.Hour)
	case "hour":
		since = ceilTo(since, time.Hour)
	case "minute":
		since = ceilTo(since, time.Minute)
	}
	rows, err := h.a.St.Usage(g, since, r.URL.Query().Get("client"), r.URL.Query().Get("preset"))
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Requests > rows[j].Requests })
	jok(w, rows)
}

// ceilTo rounds t up to the next multiple of step (UTC aligned).
func ceilTo(t time.Time, step time.Duration) time.Time {
	c := t.Truncate(step)
	if c.Before(t) {
		c = c.Add(step)
	}
	return c
}

func (h *Handler) requests(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 500 {
		n = 100
	}
	rows, _ := h.a.St.Recent(n, r.URL.Query().Get("client"))
	jok(w, rows)
}

// ---- quality scores from public leaderboards ----

func (h *Handler) benchInfo(w http.ResponseWriter, r *http.Request) {
	scores, _ := h.a.St.Bench()
	snap := h.a.Cat.Snapshot()
	type kind struct {
		Kind      string   `json:"kind"`
		Total     int      `json:"total"`
		Matched   int      `json:"matched"`
		Unmatched []string `json:"unmatched"`
	}
	var kinds []kind
	latest := ""
	for _, a := range bench.Arenas {
		k := kind{Kind: a.Endpoint}
		if snap != nil {
			for _, id := range snap.IDs() {
				if m := snap.Models[id]; m.Endpoint == a.Endpoint && !m.Free {
					k.Total++
					if _, ok := scores[id]; ok {
						k.Matched++
					} else if len(k.Unmatched) < 60 {
						k.Unmatched = append(k.Unmatched, id)
					}
				}
			}
		}
		kinds = append(kinds, k)
	}
	for _, byArena := range scores {
		for _, s := range byArena {
			if s.Date > latest {
				latest = s.Date
			}
		}
	}
	jok(w, map[string]any{"fetched": h.a.BenchFetched(), "latest": latest, "models": len(scores), "kinds": kinds, "source": "LMArena leaderboard dataset (Hugging Face), overall category"})
}

func (h *Handler) benchRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := h.a.SyncBench(ctx); err != nil {
		jerr(w, 502, "leaderboard download failed: "+err.Error())
		return
	}
	jok(w, map[string]any{"ok": true})
}
