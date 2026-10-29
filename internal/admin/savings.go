package admin

import (
	"sort"
	"strings"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/store"
)

// The "estimated saved cost" of the Usage page answers one question: what would this traffic have cost on a
// popular model, with no RTK in front of it, and how much of that did xellter not spend?
//
//	baseline = (uncached input + the tokens RTK removed) x input price + cached input x cache price + output x output price
//	saved    = baseline - what the log says was charged
//
// A model that is not a text model is compared on its own kind of work: an image model prices the images that were
// made, a speech model the thousands of characters spoken, and so on, from the units the log records (Unit, Units).
// A model only ever answers for the kinds of work it can do, and the charged side counts only that same work, so a
// speech model is never "beaten" by the cost of chat. A video is priced on the model's cheapest line of the unit.
//
// Prices are the model's list price (the log's own cost is list price too, free and plan included), so the two
// sides compare like with like. The RTK part is reported on its own because it is the one saving that does not
// depend on which model is compared with.

// charsPerToken turns the characters RTK removed into tokens, the same four-to-one rule the gateway uses to
// estimate a prompt (ADR 0031).
const charsPerToken = 4

// savingModel is one model a spend is compared with.
type savingModel struct {
	ID     string  `json:"id"`
	Found  bool    `json:"found"` // false: the catalog has no such model (yet), so nothing is priced
	InIDR  float64 `json:"in_idr"`
	OutIDR float64 `json:"out_idr"`
	Cached float64 `json:"cached_idr"`
	// Kinds says what the model can be compared on: "tokens" for text, and the catalog unit of each media line.
	Kinds []string `json:"kinds"`
	// unit is the cheapest price per catalog unit, for a media model.
	unit map[string]float64
}

// saving is the comparison of one spend (a row, or the totals) with one model.
type saving struct {
	Model    string  `json:"model"`
	Baseline float64 `json:"baseline_idr"` // what it would have cost there, without RTK
	Saved    float64 `json:"saved_idr"`    // baseline minus the actual cost; negative when xellter cost more
	Percent  float64 `json:"saved_pct"`    // saved as a share of the baseline
	RTK      float64 `json:"rtk_idr"`      // the part of the baseline that RTK removed
	// covered is what the log charged for the work this model could have done: the other side of the baseline.
	covered float64
}

// compareList reads the compare query parameter (comma separated ids); empty means the configured list.
func compareList(q string, configured []string) []string {
	if strings.TrimSpace(q) == "" {
		return configured
	}
	var out []string
	seen := map[string]bool{}
	for _, id := range strings.Split(q, ",") {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// savingModels prices the ids from the catalog snapshot.
func savingModels(snap *catalog.Snapshot, ids []string) []savingModel {
	out := make([]savingModel, 0, len(ids))
	for _, id := range ids {
		sm := savingModel{ID: id, Kinds: []string{}}
		if snap != nil {
			// System One is a service, not a text model: nothing is compared with it, and it is compared with nothing
			if m := snap.Models[id]; m != nil && m.Endpoint != "systemone" {
				if m.InIDR > 0 || m.OutIDR > 0 {
					sm.InIDR, sm.OutIDR, sm.Cached = m.InIDR, m.OutIDR, m.CachedIDR
					if sm.Cached <= 0 || sm.Cached > sm.InIDR {
						sm.Cached = sm.InIDR // no cache price published: cached tokens cost as much as any input
					}
					sm.Kinds = append(sm.Kinds, "tokens")
				}
				for _, l := range m.Lines {
					if l.IDR <= 0 {
						continue
					}
					if sm.unit == nil {
						sm.unit = map[string]float64{}
					}
					if p, ok := sm.unit[l.Unit]; !ok || l.IDR < p {
						sm.unit[l.Unit] = l.IDR
					}
				}
				units := make([]string, 0, len(sm.unit))
				for u := range sm.unit {
					units = append(units, u)
				}
				sort.Strings(units)
				sm.Kinds = append(sm.Kinds, units...)
				sm.Found = len(sm.Kinds) > 0
			}
		}
		out = append(out, sm)
	}
	return out
}

// spendOf compares one spend with each model. media is the row's non-chat work; cost is everything it was charged.
func spendOf(models []savingModel, inTok, outTok, cachedTok, rtkChars int64, cost float64, media []store.UnitSpend) []saving {
	chatCost := cost
	for _, u := range media {
		chatCost -= u.Cost
		if u.Unit == "systemone" {
			// System One is a service with its own price, not a text model: its tokens are nobody's baseline
			inTok, outTok, cachedTok, rtkChars = inTok-u.InTok, outTok-u.OutTok, cachedTok-u.CachedTok, rtkChars-u.RTKSaved
		}
	}
	inTok, outTok, cachedTok, rtkChars = max(inTok, 0), max(outTok, 0), max(cachedTok, 0), max(rtkChars, 0)
	if cachedTok > inTok {
		cachedTok = inTok
	}
	rtkTok := float64(rtkChars) / charsPerToken
	if chatCost < 0 {
		chatCost = 0
	}
	out := make([]saving, 0, len(models))
	for _, m := range models {
		s := saving{Model: m.ID}
		if m.Found {
			if m.InIDR > 0 || m.OutIDR > 0 {
				s.RTK = rtkTok * m.InIDR / 1e6
				s.Baseline += (float64(inTok-cachedTok)*m.InIDR+float64(cachedTok)*m.Cached+float64(outTok)*m.OutIDR)/1e6 + s.RTK
				s.covered += chatCost
			}
			for _, u := range media {
				if p, ok := m.unit[u.Unit]; ok {
					s.Baseline += u.Units * p
					s.covered += u.Cost
				}
			}
			s.finish()
		}
		out = append(out, s)
	}
	return out
}

func (s *saving) finish() {
	s.Saved = s.Baseline - s.covered
	s.Percent = 0
	if s.Baseline > 0 {
		s.Percent = s.Saved / s.Baseline * 100
	}
}

// sumSavings adds the savings of several rows; the percentage is recomputed from the sums.
func sumSavings(models []savingModel, rows [][]saving) []saving {
	out := make([]saving, len(models))
	for i, m := range models {
		out[i].Model = m.ID
	}
	for _, r := range rows {
		for i := range out {
			out[i].Baseline += r[i].Baseline
			out[i].RTK += r[i].RTK
			out[i].covered += r[i].covered
		}
	}
	for i := range out {
		if models[i].Found {
			out[i].finish()
		}
	}
	return out
}
