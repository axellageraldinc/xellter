// Package effort reads the reasoning effort a request asks for, and remembers which models refuse it.
//
// A client asks for an effort ("none", "low", "medium", ...). The gateway forwards it and does not choose a
// model by it (ADR 0028): measured against kenari, most models ignore "none" and a few answer HTTP 400 to it, and
// nothing in the catalog says which. The one fact worth keeping is a refusal. A model that answered 400 to the
// field is served without it from then on, and the client never sees the refusal.
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

// Refusals remembers which models refuse which effort.
//
// A refusal is permanent: answering HTTP 400 to the field is a property of the API's parameter validation, not a
// passing mood, and sending the field again would fail the call for a client that cannot drop it. It is saved
// across restarts, so the first request after one does not fail again.
type Refusals struct {
	mu      sync.RWMutex
	byModel map[string]map[Level]int64 // model -> level -> unix seconds of the refusal
	// version counts the writes that changed something, so a saver can tell whether there is anything new.
	version uint64
}

// NewRefusals builds an empty memory.
func NewRefusals() *Refusals { return &Refusals{byModel: map[string]map[Level]int64{}} }

// Refuses reports whether the model is known to answer HTTP 400 to this effort.
func (r *Refusals) Refuses(model string, l Level) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byModel[model][l]
	return ok
}

// Record remembers that a model refuses an effort.
func (r *Refusals) Record(model string, l Level, now time.Time) {
	if model == "" || l == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byModel[model][l]; ok {
		return
	}
	if r.byModel[model] == nil {
		r.byModel[model] = map[Level]int64{}
	}
	r.byModel[model][l] = now.Unix()
	r.version++
}

// Refusal is one remembered refusal, in the form that is saved.
type Refusal struct {
	Model string `json:"model"`
	Level string `json:"level"`
	At    int64  `json:"at"`
}

// All returns every refusal.
func (r *Refusals) All() []Refusal {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Refusal
	for model, levels := range r.byModel {
		for l, at := range levels {
			out = append(out, Refusal{Model: model, Level: string(l), At: at})
		}
	}
	return out
}

// Restore loads refusals saved by All and returns how many it kept.
func (r *Refusals) Restore(saved []Refusal) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, f := range saved {
		l, ok := normalize(f.Level)
		if f.Model == "" || !ok {
			continue
		}
		if r.byModel[f.Model] == nil {
			r.byModel[f.Model] = map[Level]int64{}
		}
		if _, had := r.byModel[f.Model][l]; !had {
			r.byModel[f.Model][l] = f.At
			n++
		}
	}
	if n > 0 {
		r.version++
	}
	return n
}

// Version changes whenever a refusal is added.
func (r *Refusals) Version() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}
