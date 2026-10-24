// Package rank turns the catalog, key state, health stats and JEV scores into
// an ordered candidate list per preset model, with an explanation for every
// decision. It is pure: no I/O, so it is cheap to test with fixtures.
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

// Inputs is everything a ranking run needs.
type Inputs struct {
	Cfg   *config.Config
	Snap  *catalog.Snapshot
	Pool  *upstream.Pool
	Stats map[string]store.ModelStat // recent window
	Life  map[string]store.ModelStat // all time
	JEV   map[string]store.JEVScore
	Seen  map[string]time.Time
	Bench map[string]store.BenchRow // leaderboard scores mapped onto catalog ids
	Now   time.Time
}

// Candidate is one ranked model with its explanation.
type Candidate struct {
	Model       string             `json:"model"`
	Score       float64            `json:"score"`
	Quality     float64            `json:"quality"`
	CostIDR     float64            `json:"cost_idr"`
	ListCost    float64            `json:"list_cost_idr"`
	Components  map[string]float64 `json:"components"`
	Bonuses     map[string]float64 `json:"bonuses"`
	Flags       []string           `json:"flags"`
	BenchSource string             `json:"bench_source"`
	Reason      string             `json:"reason"`
}

// Rejected explains why a model was excluded.
type Rejected struct {
	Model string `json:"model"`
	Why   string `json:"why"`
}

// Ranking is the result for one preset.
type Ranking struct {
	Client      string      `json:"client"`
	Preset      string      `json:"preset"`
	Modality    string      `json:"modality"`
	Mode        string      `json:"mode"`
	Candidates  []Candidate `json:"candidates"` // ordered main list
	Front       int         `json:"front"`      // first Front candidates are tied (round-robin group)
	Trial       []Candidate `json:"trial"`      // canary pool (new, unproven)
	Rejected    []Rejected  `json:"rejected"`
	Unavailable []string    `json:"unavailable"` // right modality, but no enabled key may call them
	Models      []string    `json:"models"`      // every model of this kind the client's keys can call, before the preset's own filters
	Callable    int         `json:"callable"`    // distinct models this client's keys can call for the preset
	Computed    time.Time   `json:"computed"`
}

// Key is the map key of a client's preset ranking.
func Key(client, preset string) string { return client + "/" + preset }

// All computes rankings for every preset of every client. A client's presets
// only consider the models its own kenari keys can call.
func All(in Inputs) map[string]*Ranking {
	out := map[string]*Ranking{}
	for i := range in.Cfg.Clients {
		cl := &in.Cfg.Clients[i]
		for name, p := range cl.Presets {
			out[Key(cl.Name, name)] = One(in, cl, name, p)
		}
	}
	return out
}

type scored struct {
	trial     bool // new or unverified and not yet proven: only receives canary traffic
	m         *catalog.Model
	cost      float64
	costKnown bool
	listCost  float64
	covered   bool
	bench     float64
	benchSrc  string
	flags     []string
}

// One ranks a single preset.
func One(in Inputs, cl *config.Client, name string, sh config.Preset) *Ranking {
	r := &Ranking{Client: cl.Name, Preset: name, Modality: sh.Modality, Mode: sh.Mode, Computed: in.Now}
	if sh.Mode == "combo" {
		return combo(in, r, sh)
	}
	rules := in.Cfg.Rules
	look := time.Duration(rules.UpcomingLookaheadHrs * float64(time.Hour))
	minRemain := time.Duration(rules.PromoMinRemainingHrs * float64(time.Hour))
	reject := func(id, why string) { r.Rejected = append(r.Rejected, Rejected{id, why}) }

	anyPlanKey := false
	for _, k := range in.Pool.ClientKeys(cl.Name) {
		if c := k.Config(); c.Enabled && c.Tier == "plan" {
			anyPlanKey = true
		}
	}

	for _, id := range in.Snap.IDs() { // what the client's keys can call at all; lets the panel offer a tick list
		if m := in.Snap.Models[id]; m.Endpoint == sh.Modality {
			for _, k := range in.Pool.ClientKeys(cl.Name) {
				if k.Config().Enabled && k.Secret != "" && k.AllowedModel(id) {
					r.Models = append(r.Models, id)
					break
				}
			}
		}
	}

	var pool []*scored
	for _, id := range in.Snap.IDs() {
		m := in.Snap.Models[id]
		if m.Endpoint != sh.Modality {
			continue
		}
		switch {
		case len(sh.Allow) > 0 && !config.MatchAny(sh.Allow, id):
			continue // silently out of scope
		case config.MatchAny(sh.Deny, id):
			reject(id, "denied by preset config")
			continue
		case sh.Require.ToolCall && !m.ToolCall:
			reject(id, "no tool calling")
			continue
		case sh.Require.Vision && !m.Vision:
			reject(id, "no vision input")
			continue
		case sh.Require.Reasoning && !m.Reasoning:
			reject(id, "no reasoning")
			continue
		case sh.Require.MinContext > 0 && m.Context < sh.Require.MinContext:
			reject(id, fmt.Sprintf("context %d < %d", m.Context, sh.Require.MinContext))
			continue
		case m.SunsetAt != nil && m.SunsetAt.Sub(in.Now) < time.Duration(rules.AvoidSunsetDays)*24*time.Hour:
			reject(id, "sunsets "+m.SunsetAt.Format("2006-01-02"))
			continue
		case m.Subscriber && !anyPlanKey:
			reject(id, "subscriber only")
			continue
		}
		s := &scored{m: m}
		usable, covered := false, false
		for _, k := range in.Pool.ClientKeys(cl.Name) {
			c := k.Config()
			if !c.Enabled || k.Secret == "" || !k.AllowedModel(id) {
				continue
			}
			usable = true
			if k.Covered(m, in.Snap, in.Now) {
				covered = true
			}
		}
		if !usable {
			r.Unavailable = append(r.Unavailable, id)
			continue
		}
		s.covered = covered
		s.listCost, s.costKnown = RefCost(m, sh.Cost, in.Now, look)
		s.cost = s.listCost
		switch {
		case m.Free:
			s.cost, s.costKnown = 0, true
		case covered: // plan quota is finite, so covered models are discounted, not free
			s.cost = s.listCost * rules.PlanCostFactor
		}
		pool = append(pool, s)
	}
	r.Callable = len(pool)
	if len(pool) == 0 {
		return r
	}
	assignBench(in, pool, sh)

	// Trial = models we have no evidence for yet. A model is on trial when it is new, or (text only) when it has
	// neither a configured benchmark, nor a price to estimate from, nor a JEV score: for such a model a "free"
	// label says nothing about quality, so it must not lead any preset until it is judged or has proven itself.
	for _, s := range pool {
		m := s.m
		isNew := false
		if t, ok := in.Seen[m.ID]; ok && t.Unix() > 0 && in.Now.Sub(t) < time.Duration(rules.NewModelDays)*24*time.Hour {
			isNew = true
		}
		life := in.Life[m.ID]
		proven := life.N >= rules.CanaryMinRequests && float64(life.OK)/math.Max(float64(life.N), 1) >= rules.CanaryMinSuccess
		_, judged := in.JEV[m.ID]
		unverified := sh.Modality == "chat" && s.benchSrc == "unknown" && !judged
		s.trial = (isNew || unverified) && !proven
		if unverified {
			s.flags = append(s.flags, "unverified")
		}
	}

	// cost score: free = 1; a paid model scores 1/(1+ln(cost/reference)) where the reference is the cheapest paid
	// model that is not on trial: equal price 1.0, 13% dearer 0.89, twice 0.59, ten times 0.30. Smooth, so small
	// price differences count (a log scale over the whole catalog made them invisible) and a very expensive model
	// does not collapse to exactly 0.
	cheapest := math.Inf(1)
	for _, s := range pool {
		if !s.trial && s.costKnown && s.cost > 0 {
			cheapest = math.Min(cheapest, s.cost)
		}
	}
	costScore := func(s *scored) float64 {
		switch {
		case !s.costKnown:
			return 0.4
		case s.cost == 0 || math.IsInf(cheapest, 1) || s.cost <= cheapest:
			return 1
		}
		return clamp(1 / (1 + math.Log(s.cost/cheapest)))
	}

	// reliability reference
	var lats []float64
	for _, s := range pool {
		if st, ok := in.Stats[s.m.ID]; ok && st.N >= 5 && st.AvgLat > 0 {
			lats = append(lats, st.AvgLat)
		}
	}
	sort.Float64s(lats)
	medLat := 0.0
	if len(lats) > 0 {
		medLat = lats[len(lats)/2]
	}

	w := sh.Weights
	// Free / plan / promo bonuses express "money matters". They scale with how much this preset cares about cost
	// (balanced = 1x, cheapest = 2x, best quality = about a tenth), so they cannot outvote quality when quality is the goal.
	bonusScale := 1.0
	if tot := w.Cost + w.Benchmark + w.JEV + w.Reliability + w.Context; tot > 0 {
		bonusScale = math.Max(0.1, math.Min(2, (w.Cost/tot)/0.30))
	}
	minCtx := float64(sh.Require.MinContext)
	if minCtx <= 0 {
		minCtx = 8000
	}
	var main, trial []Candidate
	for _, s := range pool {
		m := s.m
		comp := map[string]float64{"cost": costScore(s), "benchmark": s.bench}
		wts := map[string]float64{"cost": w.Cost, "benchmark": w.Benchmark, "reliability": w.Reliability, "context": w.Context}
		comp["context"] = clamp(math.Log(float64(max(m.Context, 1))/minCtx) / math.Log(16))
		if j, ok := in.JEV[m.ID]; ok {
			comp["jev"], wts["jev"] = j.Score, w.JEV
		} else if in.Cfg.JEV.Enabled {
			comp["jev"], wts["jev"] = 0.4, w.JEV
		}
		st, hasStat := in.Stats[m.ID]
		rel := 0.6
		if hasStat && st.N >= 5 {
			lat := 1.0
			if medLat > 0 && st.AvgLat > 0 {
				lat = math.Min(1, medLat/st.AvgLat)
			}
			rel = 0.7*float64(st.OK)/float64(st.N) + 0.3*lat
		}
		comp["reliability"] = rel

		var num, den float64
		for k, v := range comp {
			if wt := wts[k]; wt > 0 {
				num += wt * v
				den += wt
			}
		}
		score := 0.0
		if den > 0 {
			score = num / den
		}
		// Quality, for the min_quality gate: the benchmark score, blended with the JEV score only when the model has
		// really been judged. Neutral placeholders and the context size must not dilute it, otherwise a weak model
		// passes any gate (context is a hard requirement of its own).
		quality := 1.0
		if wb := wts["benchmark"]; wb > 0 {
			quality = comp["benchmark"]
			if j, judged := in.JEV[m.ID]; judged && w.JEV > 0 {
				quality = (wb*comp["benchmark"] + w.JEV*j.Score) / (wb + w.JEV)
			}
		}

		bon := map[string]float64{}
		flags := append([]string(nil), s.flags...)
		if m.Free {
			bon["free"] = rules.FreeBonus * bonusScale
			flags = append(flags, "free")
		} else if s.covered {
			bon["plan"] = rules.PlanBonus * bonusScale
			flags = append(flags, "plan")
		}
		if m.PromoActive(in.Now, minRemain) {
			bon["promo"] = rules.PromoBonusMax * bonusScale * m.Promo.PercentOff / 100
			flags = append(flags, fmt.Sprintf("promo -%.0f%% until %s", m.Promo.PercentOff, m.Promo.EndsAt.Format("01-02 15:04")))
		}
		if m.Upcoming != nil && m.Upcoming.EffectiveAt.Sub(in.Now) < look {
			flags = append(flags, "price change soon")
		}
		// plan and promo bonuses may stack, but never past what a free model gets: a paid model must not outrank a
		// free one of equal or better quality just because it is both discounted and covered
		if other, ceiling := bon["plan"]+bon["promo"], rules.FreeBonus*bonusScale; other > ceiling && other > 0 {
			f := ceiling / other
			for _, k := range []string{"plan", "promo"} {
				if v, ok := bon[k]; ok {
					bon[k] = v * f
				}
			}
		}
		for _, b := range bon {
			score += b
		}
		c := Candidate{Model: m.ID, Score: round(score), Quality: round(quality), CostIDR: round4(s.cost), ListCost: round4(s.listCost),
			Components: roundMap(comp), Bonuses: roundMap(bon), Flags: flags, BenchSource: s.benchSrc}

		jev, hasJEV := in.JEV[m.ID]
		switch {
		case hasJEV && jev.Score < in.Cfg.JEV.PassThreshold && in.Cfg.JEV.Enabled:
			reject(m.ID, fmt.Sprintf("JEV score %.2f below threshold %.2f", jev.Score, in.Cfg.JEV.PassThreshold))
		case s.trial:
			label, note := "new, on trial", "trial candidate: receives canary traffic until proven"
			if containsStr(flags, "unverified") {
				label, note = "unverified, on trial", "no quality evidence yet: judged by JEV or proven by traffic before it can lead"
			}
			c.Flags = append(c.Flags, label)
			c.Reason = describe(c, note)
			trial = append(trial, c)
		case quality < sh.MinQuality:
			reject(m.ID, fmt.Sprintf("quality %.2f below minimum %.2f", quality, sh.MinQuality))
		default:
			c.Reason = describe(c, "")
			main = append(main, c)
		}
	}
	sort.SliceStable(main, func(i, j int) bool {
		if main[i].Score != main[j].Score {
			return main[i].Score > main[j].Score
		}
		return main[i].Model < main[j].Model
	})
	if len(main) > sh.MaxCandidates {
		for _, c := range main[sh.MaxCandidates:] {
			reject(c.Model, "outside top "+fmt.Sprint(sh.MaxCandidates))
		}
		main = main[:sh.MaxCandidates]
	}
	r.Candidates = main
	r.Front = 1
	if sh.Strategy == "round_robin" {
		for i := 1; i < len(main) && main[0].Score-main[i].Score <= sh.RRBand; i++ {
			r.Front = i + 1
		}
	}
	if len(main) == 0 {
		r.Front = 0
	}
	// canary guard: a trial model may not cost more than 3x what the leader would cost at list price
	// (the floor of Rp0.7 per reference request keeps canaries possible when the leader is free)
	if len(main) > 0 {
		ref := math.Max(main[0].ListCost, 0.7)
		for _, c := range trial {
			if c.CostIDR == 0 || c.CostIDR <= ref*3 {
				r.Trial = append(r.Trial, c)
			} else {
				reject(c.Model, "trial costs more than 3x the leader")
			}
		}
	} else {
		r.Trial = trial
	}
	sort.Slice(r.Rejected, func(i, j int) bool { return r.Rejected[i].Model < r.Rejected[j].Model })
	return r
}

func combo(in Inputs, r *Ranking, sh config.Preset) *Ranking {
	for i, id := range sh.Models {
		m := in.Snap.Models[id]
		if m == nil {
			r.Rejected = append(r.Rejected, Rejected{id, "not in catalog"})
			continue
		}
		c := Candidate{Model: id, Score: round(1 - float64(i)*0.01), Flags: []string{"combo"}, Components: map[string]float64{}, Bonuses: map[string]float64{}}
		if m.Free {
			c.Flags = append(c.Flags, "free")
		}
		c.Reason = fmt.Sprintf("combo position %d", i+1)
		r.Candidates = append(r.Candidates, c)
	}
	r.Front = min(1, len(r.Candidates))
	if sh.Strategy == "round_robin" {
		r.Front = len(r.Candidates)
	}
	return r
}

// leaderboard returns the published score of a model; a ":free" twin shares the score of its paid sibling.
func leaderboard(b map[string]store.BenchRow, id string) (store.BenchRow, bool) {
	if r, ok := b[id]; ok {
		return r, true
	}
	r, ok := b[strings.TrimSuffix(id, ":free")]
	return r, ok
}

func assignBench(in Inputs, pool []*scored, sh config.Preset) {
	// price percentile as a proxy when no configured benchmark exists
	type pr struct {
		s *scored
		p float64
	}
	var priced []pr
	for _, s := range pool {
		m := s.m
		base := m
		if m.FreeSibling != "" {
			if sib := in.Snap.Models[m.FreeSibling]; sib != nil {
				base = sib
			}
		}
		p := 0.0
		if sh.Modality == "chat" {
			in, out := base.Undiscounted() // a promo must not make a model look weaker
			p = in*0.3 + out*0.7
		} else {
			p, _ = RefCost(base, sh.Cost, in.Now, 0)
		}
		if p > 0 {
			priced = append(priced, pr{s, p})
		}
	}
	sort.Slice(priced, func(i, j int) bool { return priced[i].p < priced[j].p })
	pct := map[*scored]float64{}
	for i, x := range priced {
		if len(priced) > 1 {
			pct[x.s] = float64(i) / float64(len(priced)-1)
		} else {
			pct[x.s] = 0.5
		}
	}
	for _, s := range pool {
		id := s.m.ID
		if v, ok := in.Cfg.Benchmarks[id]; ok {
			s.bench, s.benchSrc = v/100, "configured"
		} else if v, ok := in.Cfg.Benchmarks[strings.TrimSuffix(id, ":free")]; ok {
			s.bench, s.benchSrc = v/100, "configured"
		} else if b, ok := leaderboard(in.Bench, id); ok {
			s.bench, s.benchSrc = b.Value, "arena "+b.Date
		} else if p, ok := pct[s]; ok {
			s.bench, s.benchSrc = 0.25+0.6*p, "price-proxy"
		} else {
			s.bench, s.benchSrc = 0.5, "unknown"
		}
	}
}

func describe(c Candidate, extra string) string {
	var parts []string
	for _, k := range []string{"cost", "benchmark", "jev", "reliability", "context"} {
		if v, ok := c.Components[k]; ok {
			parts = append(parts, fmt.Sprintf("%s %.2f", k, v))
		}
	}
	s := fmt.Sprintf("score %.3f (%s)", c.Score, strings.Join(parts, ", "))
	if len(c.Flags) > 0 {
		s += " [" + strings.Join(c.Flags, ", ") + "]"
	}
	if extra != "" {
		s += " " + extra
	}
	return s
}

func containsStr(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func clamp(v float64) float64  { return math.Max(0, math.Min(1, v)) }
func round(v float64) float64  { return math.Round(v*1000) / 1000 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
func roundMap(m map[string]float64) map[string]float64 {
	o := make(map[string]float64, len(m))
	for k, v := range m {
		o[k] = round(v)
	}
	return o
}
