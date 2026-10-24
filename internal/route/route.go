// Package route turns a request (client + model name) into an ordered list of
// attempts (model x upstream key), applying preset rankings, round-robin
// rotation, session stickiness and canary traffic.
package route

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/rank"
	"github.com/axellageraldinc/xellter/internal/store"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// Attempt is one try: a concrete model on a concrete key.
type Attempt struct {
	Model  *catalog.Model
	Key    *upstream.Key
	Canary bool
}

// Error is a routing failure with the HTTP status to return.
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Message }

// Plan is the outcome of routing one request.
type Plan struct {
	Preset      string // empty for passthrough
	Requested   string
	Attempts    []Attempt
	Passthrough bool
	Modality    string
}

// Router holds rankings and stickiness state.
type Router struct {
	cfg      atomic.Pointer[config.Config]
	cat      *catalog.Catalog
	pool     *upstream.Pool
	st       *store.Store
	rankings atomic.Pointer[map[string]*rank.Ranking]
	rnd      func() float64

	smu    sync.Mutex
	sticky map[string]stickyEntry

	rrMu sync.Mutex
	rr   map[string]uint64 // per-preset rotation counters (kept apart from key rotation)
}

type stickyEntry struct {
	model, key string
	exp        time.Time
}

// New builds a router.
func New(cfg *config.Config, cat *catalog.Catalog, pool *upstream.Pool, st *store.Store) *Router {
	r := &Router{cat: cat, pool: pool, st: st, rnd: rand.Float64, sticky: map[string]stickyEntry{}, rr: map[string]uint64{}}
	r.cfg.Store(cfg)
	return r
}

// SetConfig swaps the configuration and recomputes rankings.
func (r *Router) SetConfig(c *config.Config) {
	r.cfg.Store(c)
	r.Recompute(time.Now())
}

// SetRand overrides the random source (tests).
func (r *Router) SetRand(f func() float64) { r.rnd = f }

// MarkSeen records first-seen dates for newly appeared catalog models.
func (r *Router) MarkSeen(snap *catalog.Snapshot) {
	if r.st == nil {
		return
	}
	seen, err := r.st.Seen()
	if err != nil {
		return
	}
	first := len(seen) == 0
	added := map[string]time.Time{}
	for _, c := range snap.Changes {
		if c.Kind == "added" {
			added[c.Model] = c.At
		}
	}
	var fresh, base []string
	for _, id := range snap.IDs() {
		if _, ok := seen[id]; ok {
			continue
		}
		if t, ok := added[id]; ok && first {
			r.st.MarkSeen([]string{id}, t.Unix())
			continue
		}
		if first {
			base = append(base, id)
		} else {
			fresh = append(fresh, id)
		}
	}
	r.st.MarkSeen(base, 0)
	r.st.MarkSeen(fresh, time.Now().Unix())
}

// Recompute rebuilds every preset ranking from current state.
func (r *Router) Recompute(now time.Time) {
	snap := r.cat.Snapshot()
	if snap == nil {
		return
	}
	cfg := r.cfg.Load()
	in := rank.Inputs{Cfg: cfg, Snap: snap, Pool: r.pool, Now: now}
	if r.st != nil {
		in.Stats, _ = r.st.Stats(now.Add(-time.Duration(cfg.Rules.StatsWindowHours) * time.Hour))
		in.Life, _ = r.st.LifetimeStats()
		in.JEV, _ = r.st.JEVScores()
		in.Seen, _ = r.st.Seen()
		in.Bench, _ = r.st.Bench()
	}
	m := rank.All(in)
	r.rankings.Store(&m)
}

// Rankings returns the latest rankings.
func (r *Router) Rankings() map[string]*rank.Ranking {
	if p := r.rankings.Load(); p != nil {
		return *p
	}
	return nil
}

// Models returns the model ids a client may list: its presets plus passthrough models.
func (r *Router) Models(cl *config.Client) (presets []string, real []string) {
	for name := range cl.Presets {
		presets = append(presets, name)
	}
	sort.Strings(presets)
	if snap := r.cat.Snapshot(); snap != nil && len(cl.Passthrough) > 0 {
		for _, id := range snap.IDs() {
			if config.MatchAny(cl.Passthrough, id) {
				real = append(real, id)
			}
		}
	}
	return
}

// Plan resolves the model name for the client into attempts.
func (r *Router) Plan(cl *config.Client, requested, session, path string, now time.Time) (*Plan, *Error) {
	cfg := r.cfg.Load()
	snap := r.cat.Snapshot()
	if snap == nil {
		return nil, &Error{503, "catalog_unavailable", "model catalog not loaded yet", 5 * time.Second}
	}
	if requested == "" {
		return nil, &Error{400, "bad_request", "missing model", 0}
	}
	p := &Plan{Requested: requested}
	reasons := map[string]string{}

	var order []orderItem
	if sh, ok := cl.Presets[requested]; ok {
		if want := modalityOfPath(path); want != "" && want != sh.Modality {
			return nil, &Error{400, "bad_request", fmt.Sprintf("%s is a %s model; this endpoint serves %s", requested, sh.Modality, want), 0}
		}
		p.Preset, p.Modality = requested, sh.Modality
		rk := r.Rankings()[rank.Key(cl.Name, requested)]
		if rk == nil || len(rk.Candidates)+len(rk.Trial) == 0 {
			return nil, &Error{503, "no_candidates", fmt.Sprintf("no eligible model for %q right now", requested), 30 * time.Second}
		}
		order = r.order(cfg, cl, rk, sh, session, now)
	} else {
		m := snap.Models[requested]
		if m == nil {
			return nil, &Error{404, "model_not_found", fmt.Sprintf("unknown model %q", requested), 0}
		}
		if !config.MatchAny(cl.Passthrough, requested) {
			return nil, &Error{403, "model_not_allowed", fmt.Sprintf("client %q may not call %q directly; use one of its presets", cl.Name, requested), 0}
		}
		p.Passthrough, p.Modality = true, m.Endpoint
		order = []orderItem{{model: m}}
	}

	max := cfg.Resilience.MaxAttempts
	for _, it := range order {
		m := snap.Models[it.model.ID]
		if m == nil {
			continue
		}
		keys := r.keysFor(cl, m, snap, now, reasons, it.key)
		perModel := 0
		for _, k := range keys {
			if len(p.Attempts) >= max || perModel >= 2 {
				break
			}
			p.Attempts = append(p.Attempts, Attempt{Model: m, Key: k, Canary: it.canary})
			perModel++
		}
		if len(p.Attempts) >= max {
			break
		}
	}
	if len(p.Attempts) == 0 {
		return nil, noCapacity(reasons)
	}
	return p, nil
}

type orderItem struct {
	model  *catalog.Model
	canary bool
	key    string // preferred key (sticky)
}

func (r *Router) order(cfg *config.Config, cl *config.Client, rk *rank.Ranking, sh config.Preset, session string, now time.Time) []orderItem {
	snap := r.cat.Snapshot()
	var out []orderItem
	seen := map[string]bool{}
	add := func(id string, canary bool, key string) {
		if m := snap.Models[id]; m != nil && !seen[id] {
			seen[id] = true
			out = append(out, orderItem{model: m, canary: canary, key: key})
		}
	}
	// stickiness keeps one session on one model/key so provider caches stay warm
	if session != "" && cfg.Rules.StickyMinutes > 0 && sh.Modality == "chat" {
		if e, ok := r.stickyGet(cl.Name+"|"+rk.Preset+"|"+session, now); ok {
			for _, c := range rk.Candidates {
				if c.Model == e.model {
					add(e.model, false, e.key)
				}
			}
		}
	}
	if len(rk.Trial) > 0 && cfg.Rules.CanaryPercent > 0 && r.rnd()*100 < cfg.Rules.CanaryPercent {
		t := rk.Trial[int(r.rotation(rk.Client+"/"+rk.Preset+"/trial"))%len(rk.Trial)]
		add(t.Model, true, "")
	}
	front := rk.Front
	if front > len(rk.Candidates) {
		front = len(rk.Candidates)
	}
	if front > 1 {
		off := int(r.rotation(rk.Client+"/"+rk.Preset) % uint64(front))
		for i := 0; i < front; i++ {
			add(rk.Candidates[(i+off)%front].Model, false, "")
		}
	}
	for _, c := range rk.Candidates {
		add(c.Model, false, "")
	}
	if len(rk.Candidates) == 0 { // only trial models exist: use them
		for _, c := range rk.Trial {
			add(c.Model, false, "")
		}
	}
	return out
}

func (r *Router) rotation(name string) uint64 {
	r.rrMu.Lock()
	defer r.rrMu.Unlock()
	r.rr[name]++
	return r.rr[name]
}

// keysFor returns usable keys for the model, best first (plan-covered / free, then rotated).
func (r *Router) keysFor(cl *config.Client, m *catalog.Model, snap *catalog.Snapshot, now time.Time, reasons map[string]string, prefer string) []*upstream.Key {
	type kc struct {
		k    *upstream.Key
		tier int
	}
	var ks []kc
	for _, k := range r.pool.ClientKeys(cl.Name) {
		if why := k.Check(m, snap, now); why != upstream.OK {
			reasons[k.Name+"/"+m.ID] = why
			continue
		}
		tier := 2
		switch {
		case k.Name == prefer:
			tier = 0
		case m.Free || k.Covered(m, snap, now):
			tier = 1
		}
		ks = append(ks, kc{k, tier})
	}
	sort.SliceStable(ks, func(i, j int) bool { return ks[i].tier < ks[j].tier })
	// rotate inside the best tier so equal keys share load
	if len(ks) > 1 {
		n := 0
		for n < len(ks) && ks[n].tier == ks[0].tier {
			n++
		}
		if n > 1 {
			off := int(r.pool.Next() % uint64(n))
			rot := append(append([]kc{}, ks[off:n]...), ks[:off]...)
			copy(ks, rot)
		}
	}
	out := make([]*upstream.Key, len(ks))
	for i, x := range ks {
		out[i] = x.k
	}
	return out
}

// Remember stores the successful model/key for a session.
func (r *Router) Remember(cl, preset, session, model, key string, now time.Time) {
	cfg := r.cfg.Load()
	if session == "" || cfg.Rules.StickyMinutes <= 0 {
		return
	}
	r.smu.Lock()
	defer r.smu.Unlock()
	if len(r.sticky) > 20000 {
		for k, e := range r.sticky {
			if now.After(e.exp) {
				delete(r.sticky, k)
			}
		}
	}
	r.sticky[cl+"|"+preset+"|"+session] = stickyEntry{model, key, now.Add(time.Duration(cfg.Rules.StickyMinutes) * time.Minute)}
}

func (r *Router) stickyGet(k string, now time.Time) (stickyEntry, bool) {
	r.smu.Lock()
	defer r.smu.Unlock()
	e, ok := r.sticky[k]
	if !ok || now.After(e.exp) {
		delete(r.sticky, k)
		return e, false
	}
	return e, true
}

func noCapacity(reasons map[string]string) *Error {
	if len(reasons) == 0 {
		return &Error{503, "no_upstream", "no upstream key is configured for this client", 30 * time.Second}
	}
	counts := map[string]int{}
	for _, why := range reasons {
		counts[why]++
	}
	var parts []string
	for why, n := range counts {
		parts = append(parts, fmt.Sprintf("%s x%d", why, n))
	}
	sort.Strings(parts)
	status := 503
	retry := 30 * time.Second
	for why := range counts {
		if why == upstream.RPMLimit || why == upstream.Cooldown {
			status, retry = 429, 10*time.Second
		}
	}
	return &Error{status, "no_capacity", "no upstream can take the request now: " + strings.Join(parts, ", "), retry}
}

func containsStr(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// modalityOfPath maps an API path to the model modality it serves ("" = unknown).
func modalityOfPath(p string) string {
	switch {
	case strings.HasPrefix(p, "/v1/chat/completions"), strings.HasPrefix(p, "/v1/responses"), strings.HasPrefix(p, "/v1/messages"):
		return "chat"
	case strings.HasPrefix(p, "/v1/images/"):
		return "images"
	case strings.HasPrefix(p, "/v1/audio/speech"):
		return "audio_speech"
	case strings.HasPrefix(p, "/v1/audio/transcriptions"):
		return "audio_transcription"
	case strings.HasPrefix(p, "/v1/videos"):
		return "videos"
	case strings.HasPrefix(p, "/v1/music"):
		return "music"
	}
	return ""
}
