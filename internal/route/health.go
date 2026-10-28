package route

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/axellageraldinc/xellter/internal/rank"
	"github.com/axellageraldinc/xellter/internal/store"
)

// Model health (ADR 0029). A model that answers too slowly, or fails too often, is taken out of the
// preset that saw it. The record is per client and preset, like every eviction: a model can be fine for one
// client and queueing for another.

type healthSample struct {
	at   time.Time
	ttft time.Duration // 0 = no latency data (not a streamed chat answer)
	ok   bool
}

type sessState struct {
	recent []bool // newest last: was the pinned model slow on that request
	avoid  map[string]bool
	at     time.Time
}

// Out is one model that is out of a preset, and why.
type Out struct {
	Model   string    `json:"model"`
	Reason  string    `json:"reason"`
	Kind    string    `json:"kind"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Strikes int       `json:"strikes"`
}

// OutOfService lists the models currently out of one preset, longest wait first.
func (r *Router) OutOfService(client, preset string, now time.Time) []Out {
	r.smu.Lock()
	defer r.smu.Unlock()
	prefix := evictKey(client, preset, "")
	var out []Out
	for k, e := range r.evict {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix && now.Before(e.until) {
			out = append(out, Out{Model: k[len(prefix):], Reason: e.reason, Kind: e.kind, Since: e.since, Until: e.until, Strikes: e.strikes})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.After(out[j].Until) })
	return out
}

// Observe records how one request of a model went and judges the model.
//
// ttft is the wait for the first token of this attempt (0 when the request has no such thing) and ok says
// whether the model answered. When the sample takes the model out of the preset the reason is returned.
// moved says the session left its pinned model for being slow (ADR 0029).
//
// The judgement needs health_min_requests requests inside the eviction window, so one bad answer never
// takes a model out. A model that comes back from a cooldown is judged on half as many, because it has
// already been seen failing, and its next cooldown is twice as long (up to evict_cooldown_minutes).
func (r *Router) Observe(client, preset, session, model string, ttft time.Duration, ok bool, now time.Time) (out string, moved bool) {
	if preset == "" || model == "" {
		return "", false
	}
	cfg := r.cfg.Load()
	rt := cfg.Routing
	key := evictKey(client, preset, model)
	r.smu.Lock()
	defer r.smu.Unlock()
	if r.health == nil {
		r.health = map[string][]healthSample{}
		r.sess = map[string]*sessState{}
	}
	moved = r.sessionLocked(client, preset, session, model, ttft, ok, now, time.Duration(rt.RepinFirstTokenS)*time.Second)
	e := r.evict[key]
	if e == nil {
		e = &eviction{}
		r.evict[key] = e
	}
	if now.Before(e.until) {
		return "", moved // a request that was already in flight when the model went out
	}
	window := time.Duration(cfg.Routing.EvictWindowMin) * time.Minute
	keep := r.health[key][:0]
	for _, x := range r.health[key] {
		if now.Sub(x.at) <= window {
			keep = append(keep, x)
		}
	}
	r.healthVer++
	keep = append(keep, healthSample{at: now, ttft: ttft, ok: ok})
	if len(keep) > 40 {
		keep = keep[len(keep)-40:]
	}
	r.health[key] = keep
	need := rt.HealthMinReqs
	if e.strikes > 0 {
		need = max(2, need/2)
	}
	if len(keep) < need {
		return "", moved
	}
	failed := 0
	var waits []time.Duration
	for _, x := range keep {
		if !x.ok {
			failed++
		} else if x.ttft > 0 {
			waits = append(waits, x.ttft)
		}
	}
	switch {
	case failed*100 >= rt.FailPercent*len(keep):
		out = fmt.Sprintf("unreliable: %d of its last %d requests failed (limit %d%%)", failed, len(keep), rt.FailPercent)
		e.kind = "unreliable"
	case len(waits) >= need && median(waits) > time.Duration(rt.SlowTTFTSecs)*time.Second:
		out = fmt.Sprintf("slow: median first token %.1fs over its last %d requests (limit %ds)", median(waits).Seconds(), len(waits), rt.SlowTTFTSecs)
		e.kind = "slow"
	default:
		e.strikes = 0 // judged again and healthy: the next time out starts from the short cooldown
		return "", moved
	}
	// A preset is never emptied: the last two models stay in service however they are doing, because a slow
	// answer is better than a refusal.
	if r.inServiceLocked(client, preset, model, now) < 2 {
		return "", moved
	}
	cool := time.Duration(rt.QuarantineMin) * time.Minute << min(e.strikes, 10)
	if limit := time.Duration(rt.EvictCooldownMin) * time.Minute; cool > limit {
		cool = limit
	}
	e.strikes++
	e.until, e.since, e.reason = now.Add(cool), now, out
	e.fails, e.replaced = nil, true
	delete(r.health, key) // measured afresh when it comes back
	r.healthVer++
	r.recordEvent(client, preset, model, e, now)
	return out, moved
}

func median(d []time.Duration) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// sessionLocked tracks how often the model a session is pinned to was slow. Two slow answers in the last
// three requests of the session move it: the pin is dropped and the model goes to the end of that session's
// order. smu is held.
func (r *Router) sessionLocked(client, preset, session, model string, ttft time.Duration, ok bool, now time.Time, slowAfter time.Duration) bool {
	if session == "" || !r.sticky(client, preset) {
		return false
	}
	k := pinKey(client, preset, session)
	if p, pinned := r.pins[k]; !pinned || p.model != model {
		return false
	}
	ss := r.sess[k]
	if ss == nil {
		if len(r.sess) > 20000 {
			for x, v := range r.sess {
				if now.Sub(v.at) > 24*time.Hour {
					delete(r.sess, x)
				}
			}
		}
		ss = &sessState{avoid: map[string]bool{}}
		r.sess[k] = ss
	}
	ss.at = now
	ss.recent = append(ss.recent, !ok || (ttft > 0 && ttft > slowAfter))
	if len(ss.recent) > 3 {
		ss.recent = ss.recent[1:]
	}
	n := 0
	for _, s := range ss.recent {
		if s {
			n++
		}
	}
	if n < 2 {
		return false
	}
	ss.avoid[model] = true
	ss.recent = nil
	delete(r.pins, k)
	r.pinVer++
	return true
}

// avoided is the set of models a session left for being slow.
func (r *Router) avoided(client, preset, session string) map[string]bool {
	if session == "" {
		return nil
	}
	r.smu.Lock()
	defer r.smu.Unlock()
	if ss := r.sess[pinKey(client, preset, session)]; ss != nil && len(ss.avoid) > 0 {
		out := make(map[string]bool, len(ss.avoid))
		for m := range ss.avoid {
			out[m] = true
		}
		return out
	}
	return nil
}

// inServiceLocked counts the models of a preset that are not out, not counting the one being judged.
func (r *Router) inServiceLocked(client, preset, except string, now time.Time) int {
	rk := r.Rankings()[rank.Key(client, preset)]
	if rk == nil {
		return 0
	}
	n := 0
	count := func(id string) {
		if e := r.evict[evictKey(client, preset, id)]; id != except && (e == nil || !now.Before(e.until)) {
			n++
		}
	}
	for _, c := range rk.Candidates {
		count(c.Model)
	}
	for _, c := range rk.Reserve {
		count(c.Model)
	}
	return n
}

// recordEvent writes down, off the request path, that a model was taken out of a preset. smu is held.
func (r *Router) recordEvent(client, preset, model string, e *eviction, now time.Time) {
	if r.st == nil {
		return
	}
	ev := store.ModelEvent{TS: now.UnixMilli(), Client: client, Preset: preset, Model: model, Kind: e.kind,
		Reason: e.reason, Until: e.until.UnixMilli(), Strikes: e.strikes}
	go r.st.AddModelEvent(ev)
}

// The health records and the models that are out are kept in the database, so a restart does not forgive a
// model that was slow a minute ago and make the first requests after it pay for the same lesson again.

type evictSnap struct {
	Key     string `json:"k"`
	Until   int64  `json:"u"`
	Since   int64  `json:"s"`
	Reason  string `json:"r"`
	Kind    string `json:"t"`
	Strikes int    `json:"n"`
}

type sampleSnap struct {
	Key  string `json:"k"`
	At   int64  `json:"a"`
	TTFT int64  `json:"w"` // milliseconds
	OK   bool   `json:"o"`
}

type healthSnap struct {
	Evict   []evictSnap  `json:"evict"`
	Samples []sampleSnap `json:"samples"`
}

// HealthState returns the models that are out (or have a record of being out) and the recent samples, as JSON,
// with a version that changes whenever any of it does.
func (r *Router) HealthState() ([]byte, uint64) {
	r.smu.Lock()
	defer r.smu.Unlock()
	var h healthSnap
	for k, e := range r.evict {
		if e.until.IsZero() && e.strikes == 0 {
			continue
		}
		h.Evict = append(h.Evict, evictSnap{Key: k, Until: e.until.UnixMilli(), Since: e.since.UnixMilli(), Reason: e.reason, Kind: e.kind, Strikes: e.strikes})
	}
	for k, list := range r.health {
		for _, x := range list {
			h.Samples = append(h.Samples, sampleSnap{Key: k, At: x.at.UnixMilli(), TTFT: x.ttft.Milliseconds(), OK: x.ok})
		}
	}
	raw, _ := json.Marshal(h)
	return raw, r.healthVer
}

// RestoreHealth loads what HealthState saved: the models still out, the strikes that make the next cooldown
// longer, and the samples that are still inside the window. It returns how many models are still out.
func (r *Router) RestoreHealth(raw []byte, now time.Time) (out int, version uint64) {
	var h healthSnap
	if len(raw) == 0 || json.Unmarshal(raw, &h) != nil {
		return 0, r.healthVersion()
	}
	window := time.Duration(r.cfg.Load().Routing.EvictWindowMin) * time.Minute
	r.smu.Lock()
	defer r.smu.Unlock()
	if r.health == nil {
		r.health = map[string][]healthSample{}
		r.sess = map[string]*sessState{}
	}
	for _, x := range h.Evict {
		if r.evict[x.Key] != nil {
			continue
		}
		e := &eviction{until: time.UnixMilli(x.Until), since: time.UnixMilli(x.Since), reason: x.Reason, kind: x.Kind, strikes: x.Strikes, replaced: true}
		if x.Until == 0 {
			e.until, e.since = time.Time{}, time.Time{}
		}
		r.evict[x.Key] = e
		if now.Before(e.until) {
			out++
		}
	}
	for _, x := range h.Samples {
		at := time.UnixMilli(x.At)
		if now.Sub(at) > window {
			continue
		}
		r.health[x.Key] = append(r.health[x.Key], healthSample{at: at, ttft: time.Duration(x.TTFT) * time.Millisecond, ok: x.OK})
	}
	r.healthVer++
	return out, r.healthVer
}

func (r *Router) healthVersion() uint64 {
	r.smu.Lock()
	defer r.smu.Unlock()
	return r.healthVer
}
