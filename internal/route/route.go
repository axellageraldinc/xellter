// Package route turns a request (client + preset name) into the attempts that
// should be tried, in order. It owns three pieces of state: the cached rankings,
// the round-robin ring per preset, and the session pin.
//
// The rules are the ones in docs/rewrite-plan.md: every model in a preset's top
// list is used in turn, a session keeps one model for its whole life, a
// retriable failure retries the same model once, and a third error inside the
// window takes a model out of that preset only (ADR 0020).
package route

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/effort"
	"github.com/axellageraldinc/xellter/internal/rank"
	"github.com/axellageraldinc/xellter/internal/store"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// Attempt is one try: a concrete model on a concrete key.
type Attempt struct {
	Model *catalog.Model
	Key   *upstream.Key
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
	Preset    string
	Requested string
	Modality  string
	Session   string
	// Model is the model this request is pinned to (the session's model, or the ring's next one). It is set
	// even when the attempts below start with a different model, because the proxy reports it to the client.
	Model    string
	Attempts []Attempt
}

// Router holds rankings, the round-robin ring, the session pin and evictions.
type Router struct {
	cfg      atomic.Pointer[config.Config]
	cat      *catalog.Catalog
	pool     *upstream.Pool
	st       *store.Store
	rankings atomic.Pointer[map[string]*rank.Ranking]
	computed atomic.Int64 // unix ms of the last Recompute

	// pools is the effective pool of every preset: the models the owner ticked
	// plus the free and promo models xellter added (ADR 0022). It is rebuilt
	// with the rankings, so the request path never recomputes it.
	poolsMu sync.RWMutex
	pools   map[string][]string

	// stickyPresets says for every preset whether a session keeps one model. It
	// is rebuilt with the rankings, so the request path only reads it (ADR 0025).
	stickyMu      sync.RWMutex
	stickyPresets map[string]bool

	rrMu sync.Mutex
	rr   map[string]uint64 // per client+preset rotation counter

	// efforts is what each model is known to do with a reasoning effort. The only
	// fact the gateway acts on is a refusal: a model that answers HTTP 400 to the
	// field is served without it (ADR 0026, ADR 0028).
	efforts *effort.Refusals

	smu  sync.Mutex
	pins map[string]pin // session key -> model
	// pinVer counts the writes to pins, so a saver can tell whether there is anything new to keep.
	pinVer uint64
	// healthVer counts the changes to the health records and evictions, so a saver knows when there is news.
	healthVer uint64
	health    map[string][]healthSample // client|preset|model -> recent requests (ADR 0029)
	sess      map[string]*sessState     // client|preset|session -> how the pinned model has been doing
	evict     map[string]*eviction
}

type pin struct {
	model string
	at    time.Time
}

// eviction is the failure record of one model inside one preset.
type eviction struct {
	fails    []time.Time
	until    time.Time
	replaced bool
	// Why the model is out, since when, and how many times in a row it came back and was put out again.
	reason  string
	kind    string // errors | slow | unreliable
	since   time.Time
	strikes int
}

// New builds a router.
func New(cfg *config.Config, cat *catalog.Catalog, pool *upstream.Pool, st *store.Store) *Router {
	r := &Router{cat: cat, pool: pool, st: st, rr: map[string]uint64{}, pins: map[string]pin{}, evict: map[string]*eviction{}, efforts: effort.NewRefusals()}
	r.cfg.Store(cfg)
	return r
}

// Efforts returns what is known about the models that refuse an effort.
func (r *Router) Efforts() *effort.Refusals { return r.efforts }

// SetConfig swaps the configuration and recomputes rankings.
func (r *Router) SetConfig(c *config.Config) {
	r.cfg.Store(c)
	r.Recompute(time.Now())
}

// Recompute rebuilds every preset ranking from current state. Eviction state is kept: a model that failed is
// still out, and the ranking simply moves on to the next candidate.
func (r *Router) Recompute(now time.Time) {
	snap := r.cat.Snapshot()
	if snap == nil {
		return
	}
	cfg := r.cfg.Load()
	in := rank.Inputs{Cfg: cfg, Snap: snap, Pool: r.pool, Now: now}
	if r.st != nil {
		in.Bench, _ = r.st.Bench()
	}
	m := rank.All(in)
	r.rankings.Store(&m)
	r.computed.Store(now.UnixMilli())
	// The effective pool of every preset is rebuilt here, so the request path
	// only reads it (ADR 0022).
	pools := make(map[string][]string, len(m))
	// The stickiness of every preset is rebuilt here too, so the request path
	// only reads it (ADR 0025).
	sticky := make(map[string]bool, len(m))
	for i := range cfg.Clients {
		cl := &cfg.Clients[i]
		for name, sh := range cl.Presets {
			pools[rank.Key(cl.Name, name)] = rank.EffectivePool(cfg, snap, r.pool, cl, sh, now)
			sticky[rank.Key(cl.Name, name)] = sh.IsSticky()
		}
	}
	r.poolsMu.Lock()
	r.pools = pools
	r.poolsMu.Unlock()
	r.stickyMu.Lock()
	r.stickyPresets = sticky
	r.stickyMu.Unlock()
}

// Pool is the models a preset can serve right now: the owner's list plus the free and promo models xellter
// added on its own (ADR 0022).
func (r *Router) Pool(client, preset string) []string { return r.effectivePool(client, preset) }

// effectivePool returns the cached pool of a preset.
func (r *Router) effectivePool(client, preset string) []string {
	r.poolsMu.RLock()
	defer r.poolsMu.RUnlock()
	return r.pools[rank.Key(client, preset)]
}

// Rankings returns the latest rankings.
func (r *Router) Rankings() map[string]*rank.Ranking {
	if p := r.rankings.Load(); p != nil {
		return *p
	}
	return nil
}

// Computed is when the rankings were last rebuilt.
func (r *Router) Computed() time.Time {
	ms := r.computed.Load()
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// Models lists the model names a client may ask for: its presets, plus the real catalog models it may call.
func (r *Router) Models(cl *config.Client) (presets []string, real []string) {
	for name := range cl.Presets {
		presets = append(presets, name)
	}
	sort.Strings(presets)
	snap := r.cat.Snapshot()
	if snap == nil {
		return
	}
	// A client may always name a model that is in one of its own preset pools: that is how a caller pins a
	// concrete model without a separate allow list. The pool includes the free and promo models xellter
	// added on its own (ADR 0022).
	own := map[string]bool{}
	for name := range cl.Presets {
		for _, id := range r.effectivePool(cl.Name, name) {
			own[id] = true
		}
	}
	for _, id := range snap.IDs() {
		if own[id] {
			real = append(real, id)
		}
	}
	return
}

// Plan resolves the requested name for the client into the attempts to try, in order.
func (r *Router) Plan(cl *config.Client, requested, session, path string, now time.Time) (*Plan, *Error) {
	return r.PlanEffort(cl, requested, session, path, now, 0)
}

// PlanEffort resolves the requested name into the attempts to try, in order.
//
// The reasoning effort a client asks for plays no part in the choice of model. It used to
// (ADR 0026): "none" moved the models seen thinking to the back of the order. That made
// the same session bounce between models, because a side call such as a title or a
// summary asks for "none" and was routed away from the model the conversation was
// pinned to, and then moved the pin with it (ADR 0028). The effort is the client's
// request to the model; the gateway forwards it, and strips it for a model known to
// refuse it.
func (r *Router) PlanEffort(cl *config.Client, requested, session, path string, now time.Time, bodyTokens int) (*Plan, *Error) {
	snap := r.cat.Snapshot()
	if snap == nil {
		return nil, &Error{503, "catalog_unavailable", "model catalog not loaded yet", 5 * time.Second}
	}
	if requested == "" {
		return nil, &Error{400, "bad_request", "missing model", 0}
	}
	// A preset serves the endpoints of its category and nothing else, and an
	// endpoint xellter does not have is served by nothing. An unknown path is
	// refused instead of being served by whichever category happens to match: a
	// request for an endpoint xellter does not have would otherwise be forwarded
	// upstream as a model call, and its failure would be counted against every
	// model of the preset (ADR 0020).
	want := modalityOfPath(path)
	if want == "" {
		return nil, &Error{404, "not_found", fmt.Sprintf("%s is not an endpoint xellter serves", path), 0}
	}
	sh, isPreset := cl.Presets[requested]
	if !isPreset {
		return r.pinModel(cl, requested, session, want, now)
	}
	if !servesCategory(want, sh.Category) {
		return nil, &Error{400, "bad_request", fmt.Sprintf("%s is a %s preset; this endpoint serves %s", requested, sh.Category, want), 0}
	}
	rk := r.Rankings()[rank.Key(cl.Name, requested)]
	if rk == nil || len(rk.Candidates) == 0 {
		return nil, &Error{503, "no_candidates", fmt.Sprintf("preset %q has no usable model right now", requested), 30 * time.Second}
	}
	p := &Plan{Preset: requested, Requested: requested, Modality: sh.Category, Session: session}
	order := r.order(cl, rk, session, now)
	pinned := ""
	if len(order) > 0 && r.sticky(cl.Name, requested) && session != "" && order[0] == r.pinned(cl.Name, requested, session) {
		pinned = order[0]
	}
	order = r.serveContext(order, snap, bodyTokens, pinned)
	if len(order) == 0 {
		return nil, &Error{503, "all_evicted", fmt.Sprintf("every model of preset %q is cooling down after errors", requested), 60 * time.Second}
	}
	p.Model = order[0]
	// Sticky first, then the rest of the ring. A retriable failure retries the same model once before the
	// next one is tried, which the proxy does by looking at the plan's per-model attempt count.
	reasons := map[string]int{}
	for _, id := range order {
		m := snap.Models[id]
		if m == nil {
			continue
		}
		for _, k := range r.keysFor(cl, m, snap, now, reasons) {
			p.Attempts = append(p.Attempts, Attempt{Model: m, Key: k})
		}
	}
	if len(p.Attempts) == 0 {
		return nil, noCapacity(r, cl, snap, now, reasons)
	}
	return p, nil
}

// serveContext puts the models whose window can hold the request first.
//
// A preset advertises the largest window in its pool (ADR 0026), so a client may send
// a conversation that fits the pool's best model but not the one the ring happens to
// pick. The models that can hold the request go first; the ones that cannot stay in
// the order, because a request that is too big for every model has to fail somewhere,
// and failing on the model with the biggest window is the friendliest failure.
//
// The size is an estimate in tokens, and the window is the model's own. The estimate
// is rough (four characters per token), so it is used to order, never to refuse: a
// request is never turned away by the gateway on a guess.
func (r *Router) serveContext(order []string, snap *catalog.Snapshot, bodyTokens int, pinned string) []string {
	if bodyTokens <= 0 || snap == nil || len(order) < 2 {
		return order
	}
	fit, small := make([]string, 0, len(order)), make([]string, 0, len(order))
	for _, id := range order {
		m := snap.Models[id]
		// The session's pinned model stays first whatever the estimate says: the estimate is rough, and a
		// model that really cannot hold the request answers with a context error and the next one takes it.
		if id == pinned || m == nil || m.Context <= 0 || bodyTokens < m.Context {
			fit = append(fit, id)
			continue
		}
		small = append(small, id)
	}
	return append(fit, small...)
}

// pinModel handles a request that names a real model instead of a preset.
// category is what the endpoint serves.
func (r *Router) pinModel(cl *config.Client, id, session, category string, now time.Time) (*Plan, *Error) {
	snap := r.cat.Snapshot()
	m := snap.Models[id]
	if m == nil {
		return nil, &Error{404, "model_not_found", fmt.Sprintf("unknown model %q", id), 0}
	}
	allowed := false
	for name := range cl.Presets {
		for _, mid := range r.effectivePool(cl.Name, name) {
			if mid == id {
				allowed = true
			}
		}
	}
	if !allowed {
		return nil, &Error{403, "model_not_allowed", fmt.Sprintf("client %q may not call %q; add it to one of its presets or use the preset name", cl.Name, id), 0}
	}
	// A named model serves the endpoints of its own kind, and no other path.
	if category != m.Endpoint {
		return nil, &Error{400, "bad_request", fmt.Sprintf("%s is a %s model; this endpoint serves %s", id, m.Endpoint, category), 0}
	}
	p := &Plan{Requested: id, Modality: m.Endpoint, Session: session, Model: id}
	reasons := map[string]int{}
	for _, k := range r.keysFor(cl, m, snap, now, reasons) {
		p.Attempts = append(p.Attempts, Attempt{Model: m, Key: k})
	}
	if len(p.Attempts) == 0 {
		return nil, noCapacity(r, cl, snap, now, reasons)
	}
	return p, nil
}

// order returns the models to try, best first: the session's pinned model, then the round-robin ring over the
// top list starting where the counter is, then the rest of the list. Evicted models are skipped.
//
// A preset that does not stick has no pinned model: every request takes the next model of the ring.
func (r *Router) order(cl *config.Client, rk *rank.Ranking, session string, now time.Time) []string {
	var out, late []string
	seen := map[string]bool{}
	avoid := r.avoided(cl.Name, rk.Preset, session)
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		if r.evicted(cl.Name, rk.Preset, id, now) {
			return
		}
		seen[id] = true
		if avoid[id] { // this session left the model for being slow: it is the last resort, not gone
			late = append(late, id)
			return
		}
		out = append(out, id)
	}
	ids := make([]string, len(rk.Candidates))
	for i, c := range rk.Candidates {
		ids[i] = c.Model
	}
	// A model outside the list is a reserve: it only serves when one above it is evicted, so it joins the end of
	// the ring instead of being dropped (ADR 0020).
	for _, c := range rk.Reserve {
		ids = append(ids, c.Model)
	}
	// The pinned model goes first, so a sticky session stays on the model it
	// started with.
	if r.sticky(cl.Name, rk.Preset) && session != "" {
		if id := r.pinned(cl.Name, rk.Preset, session); id != "" {
			add(id)
		}
	}
	if len(ids) > 0 {
		off := int(r.next(cl.Name, rk.Preset) % uint64(len(ids)))
		for i := range len(ids) {
			add(ids[(i+off)%len(ids)])
		}
	}
	return append(out, late...)
}

// sticky reports whether the preset keeps one model per session. A preset that
// is not in the map (it does not exist, or the map was not built yet) sticks,
// as it always did (ADR 0025).
func (r *Router) sticky(client, preset string) bool {
	r.stickyMu.RLock()
	defer r.stickyMu.RUnlock()
	v, ok := r.stickyPresets[rank.Key(client, preset)]
	return !ok || v
}

// next advances the round-robin counter of one preset and returns the previous value.
func (r *Router) next(client, preset string) uint64 {
	r.rrMu.Lock()
	defer r.rrMu.Unlock()
	k := client + "/" + preset
	v := r.rr[k]
	r.rr[k] = v + 1
	return v
}

// keysFor returns the usable keys for a model, best first: free or plan-covered before pay-as-you-go, and
// equal keys rotated so they share the load.
func (r *Router) keysFor(cl *config.Client, m *catalog.Model, snap *catalog.Snapshot, now time.Time, reasons map[string]int) []*upstream.Key {
	type kc struct {
		k    *upstream.Key
		tier int
	}
	var ks []kc
	for _, k := range r.pool.ClientKeys(cl.Name) {
		if why := k.Check(m, snap, now); why != upstream.OK {
			if reasons != nil {
				reasons[why]++
			}
			continue
		}
		tier := 2
		if m.Free || k.Covered(m, snap, now) {
			tier = 1
		}
		ks = append(ks, kc{k, tier})
	}
	sort.SliceStable(ks, func(i, j int) bool { return ks[i].tier < ks[j].tier })
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

// Pin remembers the model a session is using, so every later request of that session goes there.
// A preset that does not stick keeps no pin: every request takes the next model of the ring.
func (r *Router) Pin(client, preset, session, model string, now time.Time) {
	if session == "" || !r.sticky(client, preset) {
		return
	}
	r.smu.Lock()
	defer r.smu.Unlock()
	if len(r.pins) > 20000 {
		for k, p := range r.pins { // a pin older than a day is a session nobody came back to
			if now.Sub(p.at) > 24*time.Hour {
				delete(r.pins, k)
			}
		}
	}
	k := pinKey(client, preset, session)
	if old, ok := r.pins[k]; !ok || old.model != model {
		r.pinVer++
	}
	r.pins[k] = pin{model: model, at: now}
}

// PinSnapshot is one session pin in a form that can be stored.
type PinSnapshot struct {
	Key   string    `json:"k"`
	Model string    `json:"m"`
	At    time.Time `json:"t"`
}

// PinState returns every pin and a version that changes whenever a pin does. The pins live in memory, so
// the app keeps them in the database: without that, every restart of the gateway moved every conversation
// to another model.
func (r *Router) PinState() ([]PinSnapshot, uint64) {
	r.smu.Lock()
	defer r.smu.Unlock()
	out := make([]PinSnapshot, 0, len(r.pins))
	for k, p := range r.pins {
		out = append(out, PinSnapshot{Key: k, Model: p.model, At: p.at})
	}
	return out, r.pinVer
}

// RestorePins loads pins saved by a previous run, skipping the ones older than maxAge. It returns the
// version it leaves behind, which the saver compares against so a process that learns nothing writes nothing.
func (r *Router) RestorePins(saved []PinSnapshot, now time.Time, maxAge time.Duration) (int, uint64) {
	r.smu.Lock()
	defer r.smu.Unlock()
	n := 0
	for _, p := range saved {
		if p.Key == "" || p.Model == "" || now.Sub(p.At) > maxAge {
			continue
		}
		if _, ok := r.pins[p.Key]; !ok {
			r.pins[p.Key] = pin{model: p.Model, at: p.At}
			n++
		}
	}
	r.pinVer++
	return n, r.pinVer
}

// PinOf reports the model a session is pinned to ("" when it is not).
func (r *Router) PinOf(client, preset, session string) (string, time.Time) {
	r.smu.Lock()
	defer r.smu.Unlock()
	if p, ok := r.pins[pinKey(client, preset, session)]; ok {
		return p.model, p.at
	}
	return "", time.Time{}
}

// PinFor finds the pin of a session when the preset is not known: the preset and the model. It is how a
// session that is still answering its first request can be asked which model it is on, before the request
// log has a row for it.
func (r *Router) PinFor(client, session string) (preset, model string, at time.Time) {
	r.smu.Lock()
	defer r.smu.Unlock()
	prefix, suffix := client+"|", "|"+session
	for k, p := range r.pins {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, suffix) {
			mid := k[len(prefix) : len(k)-len(suffix)]
			if len(k) >= len(prefix)+len(suffix) && !strings.Contains(mid, "|") {
				return mid, p.model, p.at
			}
		}
	}
	return "", "", time.Time{}
}

func (r *Router) pinned(client, preset, session string) string {
	id, _ := r.PinOf(client, preset, session)
	return id
}

// Forget drops a session's pin, used when the pinned model was evicted.
func (r *Router) Forget(client, preset, session string) {
	r.smu.Lock()
	defer r.smu.Unlock()
	delete(r.pins, pinKey(client, preset, session))
	r.pinVer++
}

func pinKey(client, preset, session string) string { return client + "|" + preset + "|" + session }

// Failed records a failure of one model inside one preset. The third failure inside the window evicts it for
// the cooldown, and the preset's ranking simply moves on to the next model. It reports whether this failure
// evicted the model.
func (r *Router) Failed(client, preset, model string, now time.Time) bool {
	return r.FailedWhy(client, preset, model, "", now)
}

// FailedWhy is Failed with the reason of the failure, which the panel shows when the model is taken out.
func (r *Router) FailedWhy(client, preset, model, why string, now time.Time) bool {
	cfg := r.cfg.Load()
	window := time.Duration(cfg.Routing.EvictWindowMin) * time.Minute
	key := evictKey(client, preset, model)
	r.smu.Lock()
	defer r.smu.Unlock()
	e := r.evict[key]
	if e == nil {
		e = &eviction{}
		r.evict[key] = e
	}
	keep := e.fails[:0]
	for _, t := range e.fails {
		if now.Sub(t) <= window {
			keep = append(keep, t)
		}
	}
	e.fails = append(keep, now)
	if len(e.fails) >= cfg.Routing.EvictErrors {
		e.until = now.Add(time.Duration(cfg.Routing.EvictCooldownMin) * time.Minute)
		e.fails = nil
		e.replaced = true
		e.kind, e.since = "errors", now
		r.healthVer++
		e.reason = fmt.Sprintf("%d errors within %d minutes", cfg.Routing.EvictErrors, cfg.Routing.EvictWindowMin)
		if why != "" {
			e.reason += ", the last: " + why
		}
		r.recordEvent(client, preset, model, e, now)
		return true
	}
	return false
}

// Succeeded clears the failure record of a model in a preset: three errors in a row, not three ever.
func (r *Router) Succeeded(client, preset, model string) {
	key := evictKey(client, preset, model)
	r.smu.Lock()
	defer r.smu.Unlock()
	if e := r.evict[key]; e != nil {
		e.fails = nil
	}
}

// evicted reports whether a model is cooling down in this preset.
func (r *Router) evicted(client, preset, model string, now time.Time) bool {
	r.smu.Lock()
	defer r.smu.Unlock()
	e := r.evict[evictKey(client, preset, model)]
	return e != nil && now.Before(e.until)
}

// Evictions lists the models currently out of one preset, with the moment they come back.
func (r *Router) Evictions(client, preset string, now time.Time) map[string]time.Time {
	r.smu.Lock()
	defer r.smu.Unlock()
	out := map[string]time.Time{}
	prefix := client + "|" + preset + "|"
	for k, e := range r.evict {
		if strings.HasPrefix(k, prefix) && now.Before(e.until) {
			out[strings.TrimPrefix(k, prefix)] = e.until
		}
	}
	return out
}

func evictKey(client, preset, model string) string { return client + "|" + preset + "|" + model }

// servesCategory reports whether a path that serves one category can also serve another. Only the chat-shaped
// paths are shared: System One models are reached through the OpenAI endpoint.
func servesCategory(pathCategory, presetCategory string) bool {
	if pathCategory == presetCategory {
		return true
	}
	return pathCategory == "chat" && presetCategory == "systemone"
}

// noCapacity explains why nothing could be attempted, and whether the caller should retry soon. The per-model
// reasons come from the key checks; when they are empty the key itself is the problem.
func noCapacity(r *Router, cl *config.Client, snap *catalog.Snapshot, now time.Time, reasons map[string]int) *Error {
	keys := r.pool.ClientKeys(cl.Name)
	if len(keys) == 0 {
		return &Error{503, "no_upstream", "no kenari key is configured for this client", 30 * time.Second}
	}
	if len(reasons) == 0 {
		reasons = map[string]int{}
		for _, k := range keys {
			reasons[k.State(now)]++
		}
	}
	status, retry := 503, 30*time.Second
	var parts []string
	for why, n := range reasons {
		if why == "" {
			why = "no usable key"
		}
		parts = append(parts, fmt.Sprintf("%s x%d", why, n))
		switch why {
		case upstream.RPMLimit, upstream.Cooldown, upstream.BreakerOpen, upstream.NoSecret, upstream.PlanLimit:
			status, retry = 429, 10*time.Second
		}
	}
	sort.Strings(parts)
	return &Error{status, "no_capacity", "no kenari key can take the request now: " + strings.Join(parts, ", "), retry}
}

// modalityOfPath maps an API path to the preset category it serves ("" = unknown).
//
// A chat-shaped path serves both `chat` and `systemone`: kenari serves System One models through the same
// OpenAI-shaped endpoint, they are only listed separately. So the path alone cannot tell the two categories
// apart, and Plan accepts either on those paths.
func modalityOfPath(p string) string {
	switch {
	case strings.HasPrefix(p, "/v1/chat/completions"), strings.HasPrefix(p, "/v1/responses"), strings.HasPrefix(p, "/v1/messages"):
		return "chat"
	case strings.HasPrefix(p, "/v1/systemone"):
		return "systemone"
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
