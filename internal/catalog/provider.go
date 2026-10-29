package catalog

import (
	"context"
	"sync"
)

// Named is a source together with the provider it speaks for.
type Named struct {
	ID  string
	Src Source
}

// Merge returns one source out of several providers (ADR 0040). The first is the default provider: its plans,
// free tier and exchange rate are the snapshot's, and its models keep their bare ids, so every pool written
// before providers existed still resolves. The models of any other provider are listed as "<provider>/<id>".
//
// A secondary provider that cannot be reached does not fail the refresh: its models from the last good fetch
// stay, so one flaky upstream does not take its presets out of the ranking. If the first one fails, the refresh
// fails, as it always did.
func Merge(first Named, others ...Named) Source {
	return &merged{first: first, others: others, last: map[string]map[string]*Model{}}
}

type merged struct {
	first  Named
	others []Named
	mu     sync.Mutex
	last   map[string]map[string]*Model // provider -> models of its last good fetch, already qualified
}

func (m *merged) Fetch(ctx context.Context) (*Snapshot, error) {
	snap, err := m.first.Src.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	for _, mod := range snap.Models {
		if mod.Provider == "" {
			mod.Provider = m.first.ID
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.others {
		var models map[string]*Model
		if s, err := o.Src.Fetch(ctx); err == nil {
			models = qualify(o.ID, s.Models)
			m.last[o.ID] = models
		} else {
			models = m.last[o.ID]
		}
		for id, mod := range models {
			if _, taken := snap.Models[id]; !taken {
				snap.Models[id] = mod
			}
		}
	}
	return snap, nil
}

// qualify re-keys a provider's models under "<provider>/<id>".
func qualify(provider string, in map[string]*Model) map[string]*Model {
	out := make(map[string]*Model, len(in))
	for id, m := range in {
		cp := *m
		cp.ID = provider + "/" + id
		cp.Provider = provider
		if cp.UpstreamModel == "" {
			cp.UpstreamModel = id
		}
		if cp.FreeSibling != "" {
			cp.FreeSibling = provider + "/" + cp.FreeSibling
		}
		out[cp.ID] = &cp
	}
	return out
}
