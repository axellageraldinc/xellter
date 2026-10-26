// Package rank orders the models of one preset. It is pure: no I/O, no model
// calls, no randomness. The same inputs always produce the same order, which is
// what makes the panel's "why this order?" honest (ADR 0017).
//
// A preset is an explicit pool of models plus one optimization. Ranking never
// adds a model the owner did not pick and never removes one for being new,
// cheap, expensive or unknown; it only orders the pool and explains itself.
package rank

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// Billing classes, in the order a key is preferred for a model.
const (
	BillingFree    = "free"
	BillingPlan    = "plan"
	BillingBalance = "balance"
)

// Bench sources stored as arenas, next to the Arena configs (text, webdev, agent, vision, document,
// text_to_image, text_to_video). Every source is normalised to a 0..1 value where higher is better.
const (
	ArenaSWE   = "swebench"
	ArenaAider = "aider"
	ArenaASR   = "asr"
)

// purposeArenas is how a client purpose becomes one quality number for TEXT. Weights sum to 1; a source the
// model is missing is dropped and the rest renormalised, so a model with two of three sources is still
// comparable.
var purposeArenas = map[string]map[string]float64{
	"general": {"text": 1},
	"coding":  {ArenaSWE: 0.5, "webdev": 0.3, ArenaAider: 0.2},
	"agentic": {"agent": 1},
	"vision":  {"vision": 0.5, "document": 0.5},
	"bulk":    nil, // no quality metric: cost only
}

// categoryArenas is the quality source of every non-text category. A category has exactly one metric, so the
// client's purpose has no say here: an image preset is ranked on the image arena whoever owns it.
var categoryArenas = map[string]map[string]float64{
	"images":              {"text_to_image": 1},
	"videos":              {"text_to_video": 1},
	"audio_transcription": {ArenaASR: 1},
	"audio_speech":        nil, // no public benchmark: cost only, marked unranked
	"music":               nil,
	"systemone":           nil,
}

// Inputs is everything a ranking run needs.
type Inputs struct {
	Cfg   *config.Config
	Snap  *catalog.Snapshot
	Pool  *upstream.Pool
	Bench map[string]map[string]store.BenchRow // model -> arena -> score
	Now   time.Time
	Top   int // how many models the list holds (0 = Routing.TopModels)
}

// topSize is how many models a preset routes against.
func (in Inputs) topSize() int {
	if in.Top > 0 {
		return in.Top
	}
	if in.Cfg != nil && in.Cfg.Routing.TopModels > 0 {
		return in.Cfg.Routing.TopModels
	}
	return 10
}

// Candidate is one ranked model with its explanation.
type Candidate struct {
	Model       string   `json:"model"`
	Score       float64  `json:"score"`
	CostIDR     float64  `json:"cost_idr"` // one reference request, promo applied
	ListCostIDR float64  `json:"list_cost_idr"`
	Billing     string   `json:"billing"` // free | plan | balance
	Bench       float64  `json:"bench"`
	BenchSource string   `json:"bench_source"` // "arena text", "coding (swebench, webdev)", "none"
	Flags       []string `json:"flags"`
	Reason      string   `json:"reason"`
}

// Rejected explains why a model the owner picked is not in the list.
type Rejected struct {
	Model string `json:"model"`
	Why   string `json:"why"`
}

// Ranking is the result for one preset.
type Ranking struct {
	Client       string      `json:"client"`
	Preset       string      `json:"preset"`
	Category     string      `json:"category"`
	Optimization string      `json:"optimization"`
	Purpose      string      `json:"purpose"`
	Candidates   []Candidate `json:"candidates"` // at most Routing.TopModels, best first
	Rejected     []Rejected  `json:"rejected"`
	Unranked     []string    `json:"unranked"` // in the pool, no quality evidence anywhere
	Reserve      []Candidate `json:"reserve"`  // ordered pool members outside the list, promoted when one is evicted
	Computed     time.Time   `json:"computed"`
}

// Key is the map key of a client's preset ranking.
func Key(client, preset string) string { return client + "/" + preset }

// All computes the ranking of every preset of every client.
func All(in Inputs) map[string]*Ranking {
	out := map[string]*Ranking{}
	for i := range in.Cfg.Clients {
		cl := &in.Cfg.Clients[i]
		if !cl.Enabled {
			continue
		}
		for name, sh := range cl.Presets {
			out[Key(cl.Name, name)] = One(in, cl, name, sh)
		}
	}
	return out
}

// Reference request profiles per category. They turn several prices into one number so input, output and
// cache read are never compared in isolation.
const (
	refInTok   = 4000.0
	refOutTok  = 600.0
	refCached  = 0.7 // share of the input tokens that are cache reads
	refImages  = 1.0
	refSeconds = 5.0
	refVideoV  = "720p"
	refChars   = 1000.0
)

// RefCost is the price in IDR of one reference request of a category at the prices in effect now, with a
// running promo applied. The bool is false when the catalog publishes no usable price.
func RefCost(m *catalog.Model, category string, now time.Time) (float64, bool) {
	if m.Free {
		return 0, true
	}
	switch category {
	case "chat", "systemone":
		in, out, cached := m.Effective(now, 0)
		if in == 0 && out == 0 {
			return 0, false
		}
		return (refInTok*(1-refCached)*in + refInTok*refCached*cached + refOutTok*out) / 1e6, true
	case "images":
		if l := pickLine(m, "image", ""); l != nil {
			return l.IDR * refImages, true
		}
	case "videos":
		if l := pickLine(m, "second", refVideoV); l != nil {
			return l.IDR * refSeconds, true
		}
	case "audio_speech":
		if l := pickLine(m, "1k_chars", ""); l != nil {
			return l.IDR * refChars / 1000, true
		}
	case "audio_transcription":
		if l := pickLine(m, "second", ""); l != nil {
			return l.IDR * 60, true
		}
	case "music":
		if l := pickLine(m, "song", ""); l != nil {
			return l.IDR, true
		}
	}
	return 0, false
}

// pickLine returns the price line of a unit, preferring the variant, else the cheapest.
func pickLine(m *catalog.Model, unit, variant string) *catalog.Line {
	var best *catalog.Line
	for i := range m.Lines {
		l := &m.Lines[i]
		if l.Unit != unit {
			continue
		}
		if variant != "" && l.Variant == variant {
			return l
		}
		if best == nil || l.IDR < best.IDR {
			best = l
		}
	}
	return best
}

// costScore turns a price into 0..1 against the cheapest comparable model: equal price 1, 13% dearer 0.89,
// twice 0.59, ten times 0.30. Free is 1. Unknown price is 0.4 (a middle guess, flagged).
func costScore(cost float64, known bool, cheapest float64) float64 {
	switch {
	case !known:
		return 0.4
	case cost == 0 || math.IsInf(cheapest, 1) || cost <= cheapest:
		return 1
	}
	return clamp(1 / (1 + math.Log(cost/cheapest)))
}

type scored struct {
	m        *catalog.Model
	cost     float64
	costOK   bool
	listCost float64
	billing  string
	bench    float64
	source   string
	flags    []string
	score    float64
}

// One ranks a single preset.
func One(in Inputs, cl *config.Client, name string, sh config.Preset) *Ranking {
	r := &Ranking{
		Client: cl.Name, Preset: name, Category: sh.Category,
		Optimization: sh.Optimization, Purpose: cl.Purpose, Computed: in.Now,
	}
	reject := func(id, why string) { r.Rejected = append(r.Rejected, Rejected{id, why}) }

	// A subscriber-only model needs a plan on some key of this client.
	anyPlan := false
	for _, k := range in.Pool.ClientKeys(cl.Name) {
		if k.Account().Tier(in.Now) == "plan" {
			anyPlan = true
		}
	}

	seen := map[string]bool{}
	var pool []*scored
	for _, id := range sh.Models {
		if seen[id] {
			continue
		}
		seen[id] = true
		m := in.Snap.Models[id]
		if m == nil {
			reject(id, "not in the kenari catalog")
			continue
		}
		if !endpointMatches(m, sh.Category) {
			reject(id, "is a "+m.Endpoint+" model, not "+sh.Category)
			continue
		}
		if m.Subscriber && !anyPlan {
			reject(id, "subscriber only and the account has no plan")
			continue
		}
		if m.SunsetAt != nil && m.SunsetAt.Sub(in.Now) < time.Duration(in.Cfg.Routing.AvoidSunsetDays)*24*time.Hour {
			reject(id, "sunsets "+m.SunsetAt.Format("2006-01-02"))
			continue
		}
		billing, ok := billingOf(in, cl, m)
		if !ok {
			reject(id, "no key of this client may call it")
			continue
		}
		s := &scored{m: m, billing: billing}
		s.listCost, s.costOK = RefCost(m, sh.Category, in.Now)
		s.cost = s.listCost
		if m.Free {
			s.cost, s.costOK = 0, true
		}
		// A plan-covered model is priced at its promo-effective rate: that is what kenari deducts from the
		// plan balance. It is not zero, because the plan budget is finite.
		pool = append(pool, s)
	}
	if len(pool) == 0 {
		sortRejected(r)
		return r
	}

	assignBench(in, sh.Category, cl.Purpose, pool)

	cheapest := math.Inf(1)
	for _, s := range pool {
		if s.costOK && s.cost > 0 {
			cheapest = math.Min(cheapest, s.cost)
		}
	}
	for _, s := range pool {
		s.score = costScore(s.cost, s.costOK, cheapest)
	}

	top := in.topSize()
	switch sh.Optimization {
	case "cheapest":
		orderCheapest(r, pool, top)
	case "best_quality":
		orderQuality(r, pool, top)
	default:
		orderBalanced(r, pool, top)
	}
	// Whatever the optimization left out is reported with a reason, so "why is this model not serving?" is
	// always answerable from the panel. A model that was merely pushed out by the list size is a *reserve*: the
	// router promotes it when a model above it is evicted (ADR 0020).
	kept := map[string]bool{}
	for _, c := range r.Candidates {
		kept[c.Model] = true
	}
	for _, s := range pool {
		if kept[s.m.ID] {
			continue
		}
		already := false
		for _, x := range r.Rejected {
			if x.Model == s.m.ID {
				already = true
				break
			}
		}
		if already {
			continue
		}
		// The reserve keeps the optimization's order, so promotion is "the next best model", not an arbitrary one.
		s.score = reserveScore(s, sh.Optimization)
		r.Reserve = append(r.Reserve, Candidate{Model: s.m.ID, Score: round(s.score), CostIDR: round4(s.cost),
			ListCostIDR: round4(s.listCost), Billing: s.billing, Bench: round(s.bench), BenchSource: s.source, Flags: s.flags,
			Reason: fmt.Sprintf("reserve: outside the top %d (%s)", top, reserveWhy(sh.Optimization))})
	}
	sort.SliceStable(r.Reserve, func(i, j int) bool {
		if r.Reserve[i].Score != r.Reserve[j].Score {
			return r.Reserve[i].Score > r.Reserve[j].Score
		}
		return r.Reserve[i].Model < r.Reserve[j].Model
	})
	sortRejected(r)
	return r
}

// endpointMatches reports whether a catalog model can serve a preset category. systemone models live on the
// chat endpoint at kenari, so the category maps onto the endpoint it was fetched from.
func endpointMatches(m *catalog.Model, category string) bool {
	if category == "systemone" {
		return m.Endpoint == "systemone"
	}
	return m.Endpoint == category
}

// billingOf reports how this client would pay for the model and whether any of its keys may call it.
func billingOf(in Inputs, cl *config.Client, m *catalog.Model) (string, bool) {
	usable, plan := false, false
	for _, k := range in.Pool.ClientKeys(cl.Name) {
		if k.Secret() == "" || !k.AllowedModel(m.ID) {
			continue
		}
		usable = true
		if !m.Free && k.Covered(m, in.Snap, in.Now) {
			plan = true
		}
	}
	switch {
	case !usable:
		return "", false
	case m.Free:
		return BillingFree, true
	case plan:
		return BillingPlan, true
	}
	return BillingBalance, true
}

// assignBench fills the quality number of every candidate. The purpose decides the sources for text; every
// other category has one metric of its own and the purpose is irrelevant.
func assignBench(in Inputs, category, purpose string, pool []*scored) {
	weights := purposeArenas[purpose]
	if category != "chat" {
		weights = categoryArenas[category]
	}
	// No quality metric at all (bulk, TTS, music, System One): nothing to look up.
	if weights == nil {
		for _, s := range pool {
			s.source = "none"
		}
		return
	}
	for _, s := range pool {
		// ":free" twins share the published score of their paid sibling.
		ids := []string{s.m.ID, strings.TrimSuffix(s.m.ID, ":free")}
		var sum, wsum float64
		var used []string
		for _, arena := range sortedKeys(weights) {
			if v, ok := lookup(in, ids, arena); ok {
				sum += weights[arena] * v
				wsum += weights[arena]
				used = append(used, arena)
			}
		}
		if wsum > 0 {
			s.bench = sum / wsum
			s.source = strings.Join(used, ", ")
			continue
		}
		// Fallback, never exclusion: a text model with no purpose-specific score is scored on the general
		// arena and says so. A model with nothing anywhere is unranked (ADR 0018).
		if purpose != "general" && purpose != "bulk" {
			if v, ok := lookup(in, ids, "text"); ok {
				s.bench, s.source = v, "general (fallback)"
				s.flags = append(s.flags, "general score")
				continue
			}
		}
		s.source = "none"
	}
}

func lookup(in Inputs, ids []string, arena string) (float64, bool) {
	for _, id := range ids {
		if rows, ok := in.Bench[id]; ok {
			if row, ok := rows[arena]; ok {
				return row.Value, true
			}
		}
	}
	return 0, false
}

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// orderCheapest fills the top list from two pools: five free models and five plan-covered ones, each half
// ordered by its own reference cost, so a free model never competes with a plan-covered one. A half that
// cannot fill its share is topped up from the other half, then from pay-as-you-go models by cost.
func orderCheapest(r *Ranking, pool []*scored, top int) {
	byCost := func(list []*scored) {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].cost != list[j].cost {
				return list[i].cost < list[j].cost
			}
			return list[i].m.ID < list[j].m.ID
		})
	}
	var free, plan, balance []*scored
	for _, s := range pool {
		switch s.billing {
		case BillingFree:
			free = append(free, s)
		case BillingPlan:
			plan = append(plan, s)
		default:
			balance = append(balance, s)
		}
	}
	for _, list := range [][]*scored{free, plan, balance} {
		byCost(list)
	}
	half := max(1, top/2)
	list := take(&free, half)
	list = append(list, take(&plan, half)...)
	// top up from whatever is left, cheapest first, so an empty half never leaves the list short
	var rest []*scored
	rest = append(rest, free...)
	rest = append(rest, plan...)
	rest = append(rest, balance...)
	byCost(rest)
	list = append(list, take(&rest, top-len(list))...)
	// The score is the cost score, kept for display and for the panel's ordering; the order itself is the price.
	emit(r, list, "cheapest: reference request price, free then plan-covered")
}

// orderBalanced is cost 50 : benchmark 50.
func orderBalanced(r *Ranking, pool []*scored, top int) {
	for _, s := range pool {
		s.score = 0.5*s.score + 0.5*s.bench
	}
	sortScore(pool)
	emit(r, take(&pool, top), "balanced: 50% cost, 50% quality")
}

// orderQuality ranks on the benchmark alone. A model with no quality evidence cannot be ranked on quality, so
// it is reported as unranked instead of being given a made-up number. There is a floor: a model below the
// pool's median quality is not offered, because "best quality" that includes the worst model is not best.
func orderQuality(r *Ranking, pool []*scored, top int) {
	var ranked, unranked []*scored
	for _, s := range pool {
		if s.source == "none" {
			unranked = append(unranked, s)
			continue
		}
		s.score = s.bench
		ranked = append(ranked, s)
	}
	for _, s := range unranked {
		r.Unranked = append(r.Unranked, s.m.ID)
		r.Rejected = append(r.Rejected, Rejected{s.m.ID, "no quality evidence for " + r.Category})
	}
	if len(ranked) == 0 {
		return
	}
	sortScore(ranked)
	floor := median(ranked)
	var kept []*scored
	for _, s := range ranked {
		if s.bench < floor {
			r.Rejected = append(r.Rejected, Rejected{s.m.ID, fmt.Sprintf("quality %.2f below the pool median %.2f", s.bench, floor)})
			continue
		}
		kept = append(kept, s)
	}
	emit(r, take(&kept, top), fmt.Sprintf("best quality: %s only, floor %.2f", r.Category, floor))
}

// reserveScore is how a model outside the list would be ordered: the same rule the optimization used.
func reserveScore(s *scored, optimization string) float64 {
	switch optimization {
	case "best_quality":
		return s.bench
	case "cheapest":
		// price ascending becomes score descending, so the cheapest reserve is promoted first
		return 1 / (1 + s.cost/1000)
	}
	return 0.5*s.score + 0.5*s.bench
}

func reserveWhy(optimization string) string {
	switch optimization {
	case "best_quality":
		return "best quality first"
	case "cheapest":
		return "cheapest first"
	}
	return "balanced first"
}

func sortScore(list []*scored) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		if list[i].cost != list[j].cost {
			return list[i].cost < list[j].cost
		}
		return list[i].m.ID < list[j].m.ID
	})
}

func median(list []*scored) float64 {
	if len(list) == 0 {
		return 0
	}
	v := make([]float64, len(list))
	for i, s := range list {
		v[i] = s.bench
	}
	sort.Float64s(v)
	return v[len(v)/2]
}

// take removes up to n models from the front of list and returns them.
func take(list *[]*scored, n int) []*scored {
	if n <= 0 {
		return nil
	}
	if n > len(*list) {
		n = len(*list)
	}
	out := append([]*scored(nil), (*list)[:n]...)
	*list = (*list)[n:]
	return out
}

// emit turns scored models into candidates with their explanation.
func emit(r *Ranking, list []*scored, extra string) {
	for _, s := range list {
		c := Candidate{
			Model: s.m.ID, Score: round(s.score), CostIDR: round4(s.cost), ListCostIDR: round4(s.listCost),
			Billing: s.billing, Bench: round(s.bench), BenchSource: s.source, Flags: s.flags,
		}
		c.Reason = describe(c, extra)
		r.Candidates = append(r.Candidates, c)
	}
}

func describe(c Candidate, extra string) string {
	parts := []string{fmt.Sprintf("cost %.2f", c.CostIDR), fmt.Sprintf("quality %.2f", c.Bench), c.Billing}
	s := fmt.Sprintf("score %.3f (%s)", c.Score, strings.Join(parts, ", "))
	if c.BenchSource != "" && c.BenchSource != "none" {
		s += " from " + c.BenchSource
	}
	if len(c.Flags) > 0 {
		s += " [" + strings.Join(c.Flags, ", ") + "]"
	}
	if extra != "" {
		s += " — " + extra
	}
	return s
}

func sortRejected(r *Ranking) {
	sort.Slice(r.Rejected, func(i, j int) bool { return r.Rejected[i].Model < r.Rejected[j].Model })
	sort.Strings(r.Unranked)
}

func clamp(v float64) float64  { return math.Max(0, math.Min(1, v)) }
func round(v float64) float64  { return math.Round(v*1000) / 1000 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
