// Package effort knows what a reasoning effort means to an upstream model.
//
// A client asks for an effort ("none", "low", "medium", ...) and the gateway has to
// make that request true. Forwarding the field is not enough: measured against kenari,
// some models ignore "none" and keep thinking, and some reject the field outright with
// HTTP 400 (ADR 0026).
//
// Nothing in the catalog predicts this. Two models with byte-identical kenari metadata
// (agnes-2-5-flash:free and beam-1-0:free) differ: the first stops thinking when asked,
// the second does not. A model that publishes "none" among its supported levels can still
// reject it. So the answer is measured, not declared: the gateway watches what a model
// actually does and remembers it.
//
// The three facts a model can have:
//   - it accepts the field and honours it (thinking stops),
//   - it accepts the field and ignores it (thinking continues),
//   - it rejects the field (HTTP 400; the field must not be sent).
package effort

import (
	"strings"
	"sync"
	"time"
)

// Level is a reasoning effort as a client names it.
type Level string

// The levels a client may ask for. The set matches the [OI]-compatible wire vocabulary.
const (
	None    Level = "none"
	Minimal Level = "minimal"
	Low     Level = "low"
	Medium  Level = "medium"
	High    Level = "high"
	XHigh   Level = "xhigh"
	Max     Level = "max"
)

// Levels are the efforts a client may send, from weakest to strongest.
var Levels = []Level{None, Minimal, Low, Medium, High, XHigh, Max}

// Parse reads the effort a request asks for. It accepts the two shapes a client uses:
// the flat "reasoning_effort" and the nested "reasoning.effort". The bool says whether
// the request asked for an effort at all: a request that says nothing is not the same as
// a request that says "none", because an unset effort lets the upstream apply its own
// default (which for a hosted reasoning model can be its ceiling).
func Parse(body map[string]any) (Level, bool) {
	if body == nil {
		return "", false
	}
	if s, ok := body["reasoning_effort"].(string); ok {
		if l, ok := normalize(s); ok {
			return l, true
		}
		return "", false
	}
	if r, ok := body["reasoning"].(map[string]any); ok {
		if s, ok := r["effort"].(string); ok {
			if l, ok := normalize(s); ok {
				return l, true
			}
		}
	}
	return "", false
}

// normalize maps a wire value to a known level. An unknown value is not an effort this
// gateway understands, so it is refused rather than guessed at.
func normalize(s string) (Level, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", false
	}
	for _, l := range Levels {
		if string(l) == s {
			return l, true
		}
	}
	return "", false
}

// Verdict is what the gateway has learned about one model.
type Verdict int

const (
	// Unknown means the model has not been observed yet, so nothing is assumed.
	Unknown Verdict = iota
	// Honours means the model stops thinking when asked.
	Honours
	// Ignores means the model accepts the field but keeps thinking.
	Ignores
	// Rejects means the model answers HTTP 400 when the field is present.
	Rejects
)

// String names the verdict for the panel and the logs.
func (v Verdict) String() string {
	switch v {
	case Honours:
		return "honours"
	case Ignores:
		return "ignores"
	case Rejects:
		return "rejects"
	}
	return "unknown"
}

// observation is one measured fact about a model.
type observation struct {
	verdict Verdict
	at      time.Time
}

// Registry remembers what each model does with an effort.
//
// It is built from traffic, so it starts empty and fills up as the gateway serves
// requests. A model nobody has called is Unknown, and an Unknown model is served as the
// client asked: the gateway does not second-guess an upstream it has no evidence about.
type Registry struct {
	mu      sync.RWMutex
	byModel map[string]map[Level]observation
	// thinkers are the models observed producing reasoning at all. Without this a
	// model that stayed quiet under "none" would look like one that obeys it, when
	// the truth may be that it simply had nothing to think about.
	thinkers map[string]bool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{byModel: map[string]map[Level]observation{}, thinkers: map[string]bool{}}
}

// Verdict returns what is known about a model at a level.
func (r *Registry) Verdict(model string, l Level) Verdict {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.verdictLocked(model, l)
}

func (r *Registry) verdictLocked(model string, l Level) Verdict {
	obs, ok := r.byModel[model]
	if !ok {
		return Unknown
	}
	o, ok := obs[l]
	if !ok {
		return Unknown
	}
	return o.verdict
}

// Knows reports whether the model has been observed at this level.
func (r *Registry) Knows(model string, l Level) bool {
	return r.Verdict(model, l) != Unknown
}

// Record remembers a measured fact about a model at a level.
//
// A refusal is remembered for good: a model that answers HTTP 400 to the field will do
// it again, and the gateway must not send it again. The other two verdicts are kept for
// a while, because an upstream that starts honouring an effort (or stops) should not be
// pinned to yesterday's behaviour.
func (r *Registry) Record(model string, l Level, v Verdict, now time.Time) {
	if model == "" || l == "" || v == Unknown {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	obs, ok := r.byModel[model]
	if !ok {
		obs = map[Level]observation{}
		r.byModel[model] = obs
	}
	prev, had := obs[l]
	if v == Rejects {
		// A refusal is permanent.
		if had && prev.verdict == Rejects {
			return
		}
		obs[l] = observation{verdict: Rejects, at: now}
		return
	}
	if had && prev.verdict == Rejects {
		return // a refusal is not undone by a later success
	}
	obs[l] = observation{verdict: v, at: now}
}

// Observe records what a model actually did with an effort, and returns the verdict it
// concluded.
//
// The reasoning characters in the response are the evidence. A model that thought is
// known to think, whatever the effort was; a model that stayed quiet obeyed it, unless
// it never thought anything to begin with. A model that was never seen thinking is not
// credited with obeying: it is Unknown, and the gateway keeps serving it as the client
// asked, because there is no evidence either way.
func (r *Registry) Observe(model string, l Level, thought bool, now time.Time) Verdict {
	if model == "" || l == "" {
		return Unknown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if thought {
		r.thinkers[model] = true
	}
	obs, ok := r.byModel[model]
	if !ok {
		obs = map[Level]observation{}
		r.byModel[model] = obs
	}
	prev, had := obs[l]
	if had && prev.verdict == Rejects {
		return Rejects // a refusal is not undone by a later success
	}
	var v Verdict
	switch {
	case thought:
		v = Ignores
	case r.thinkers[model]:
		// The model is known to think, and it did not think this time: it obeyed.
		v = Honours
	default:
		// It stayed quiet, but it has never been seen thinking, so nothing is proven.
		v = Unknown
	}
	if v == Unknown {
		return Unknown
	}
	obs[l] = observation{verdict: v, at: now}
	return v
}

// Thinks reports whether the model was ever observed producing reasoning.
func (r *Registry) Thinks(model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.thinkers[model]
}

// Stale reports whether an observation is old enough to be doubted. A model that
// changed its behaviour should be re-measured, so the registry keeps the last fact
// only for this long.
func (r *Registry) Stale(model string, l Level, now time.Time, ttl time.Duration) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	obs, ok := r.byModel[model]
	if !ok {
		return false
	}
	o, ok := obs[l]
	if !ok {
		return false
	}
	return now.Sub(o.at) > ttl
}

// Snapshot is the registry's whole knowledge, for the panel and the admin API.
type Snapshot struct {
	Model string `json:"model"`
	Level string `json:"level"`
	Known string `json:"known"` // unknown | honours | ignores | rejects
	At    int64  `json:"at"`    // unix seconds of the measurement
}

// All returns every fact the registry holds.
func (r *Registry) All() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Snapshot, 0, len(r.byModel))
	for model, obs := range r.byModel {
		for l, o := range obs {
			out = append(out, Snapshot{Model: model, Level: string(l), Known: o.verdict.String(), At: o.at.Unix()})
		}
	}
	return out
}
