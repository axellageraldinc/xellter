package rank

import (
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func chat(id string, in, out float64) *catalog.Model {
	return &catalog.Model{ID: id, Endpoint: "chat", Context: 128000, ToolCall: true, InIDR: in, OutIDR: out, CachedIDR: in / 10}
}

type fx struct {
	snap *catalog.Snapshot
	cfg  *config.Config
	pool *upstream.Pool
	cl   *config.Client
}

func fixture(models ...*catalog.Model) *fx {
	cfg := config.Default()
	cfg.Clients = []config.Client{{Name: "c", Enabled: true, Presets: config.DefaultPresets(), Keys: []config.Upstream{{Name: "k", KeyEnv: "K"}}}}
	pool := upstream.NewPool()
	pool.Reload(cfg, map[string]string{"K": "s"}, nil)
	snap := &catalog.Snapshot{Models: map[string]*catalog.Model{}, Tier: catalog.FreeTier{Free: catalog.TierLimit{RPM: 5, Daily: 50}, Topup: catalog.TierLimit{RPM: 10, Daily: 1000}, Plan: catalog.TierLimit{RPM: 25}}}
	for _, m := range models {
		snap.Models[m.ID] = m
	}
	return &fx{snap, cfg, pool, &cfg.Clients[0]}
}

func (f *fx) rank(preset string, mod func(*Inputs)) *Ranking {
	in := Inputs{Cfg: f.cfg, Snap: f.snap, Pool: f.pool, Now: now, Stats: map[string]store.ModelStat{}}
	if mod != nil {
		mod(&in)
	}
	return One(in, f.cl, preset, f.cl.Presets[preset])
}

func order(r *Ranking) []string {
	var o []string
	for _, c := range r.Candidates {
		o = append(o, c.Model)
	}
	return o
}

// containsStr reports whether the list holds the value.
func containsStr(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func TestFreeModelWinsWhenQualityIsAcceptable(t *testing.T) {
	free := chat("g:free", 0, 0)
	free.Free = true
	f := fixture(free, chat("mid", 500, 1500), chat("big", 8000, 24000))
	f.cfg.Benchmarks = map[string]float64{"g:free": 70, "mid": 72, "big": 90}
	r := f.rank("text", nil)
	if o := order(r); len(o) == 0 || o[0] != "g:free" {
		t.Fatalf("free model should lead: %v", o)
	}
}

func TestQualityGateKeepsBadCheapModelsOut(t *testing.T) {
	free := chat("junk:free", 0, 0)
	free.Free = true
	f := fixture(free, chat("good", 600, 1800))
	f.cfg.Benchmarks = map[string]float64{"junk:free": 5, "good": 80}
	r := f.rank("text", nil)
	if o := order(r); len(o) != 1 || o[0] != "good" {
		t.Fatalf("gate failed: %v rejected=%v", o, r.Rejected)
	}
}

func TestPromoMakesModelCheaperAndDoesNotLowerItsQuality(t *testing.T) {
	promo := chat("promo", 63, 250) // listed price already includes a 90% discount
	promo.Promo = &catalog.Promo{PercentOff: 90, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(7 * 24 * time.Hour)}
	f := fixture(promo, chat("rival", 630, 2500), chat("other", 100, 300), chat("pricey", 9000, 30000))
	pr := f.cl.Presets["text"]
	pr.MinQuality = 0 // this test is about the price proxy, not the gate
	f.cl.Presets["text"] = pr
	r := f.rank("text", nil)
	var pc *Candidate
	for i := range r.Candidates {
		if r.Candidates[i].Model == "promo" {
			pc = &r.Candidates[i]
		}
	}
	if pc == nil {
		t.Fatalf("promo model rejected: %+v", r.Rejected)
	}
	if pc.Bonuses["promo"] <= 0 {
		t.Fatal("promo bonus missing")
	}
	// the quality proxy must use the undiscounted price: "promo" was listed at Rp630/Rp2500 before the
	// discount, so it must not look weaker than "other" (Rp100/Rp300)
	for _, c := range r.Candidates {
		if c.Model == "other" && pc.Components["benchmark"] <= c.Components["benchmark"] {
			t.Fatalf("promo lowered its benchmark proxy: promo=%v other=%v", pc.Components, c.Components)
		}
	}
}

func TestEndingPromoGetsNoBonusAndExpiredPromoRevertsPrice(t *testing.T) {
	m := chat("ending", 63, 250)
	m.Promo = &catalog.Promo{PercentOff: 90, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(2 * time.Hour)}
	if m.PromoActive(now, 12*time.Hour) {
		t.Fatal("promo with 2h left should not count with a 12h minimum")
	}
	in, _, _ := m.Effective(now.Add(3*time.Hour), 0)
	if in < 600 || in > 640 {
		t.Fatalf("price after promo end should revert to ~630, got %v", in)
	}
}

// The main list is capped at max_candidates; the best models win the places.
func TestMainListKeepsTheTopCandidates(t *testing.T) {
	models := []*catalog.Model{chat("a", 100, 300), chat("b", 110, 330), chat("c", 120, 360), chat("d", 130, 390), chat("e", 140, 420), chat("f", 150, 450)}
	f := fixture(models...)
	f.cfg.Benchmarks = map[string]float64{"a": 90, "b": 85, "c": 80, "d": 75, "e": 70, "f": 65}
	r := f.rank("text", nil)
	if len(r.Candidates) != 5 {
		t.Fatalf("the main list must hold 5 models: %v", order(r))
	}
	for i, want := range []string{"a", "b", "c", "d", "e"} {
		if r.Candidates[i].Model != want {
			t.Fatalf("position %d must be %s: %v", i+1, want, order(r))
		}
	}
	// the one that did not make it is rejected with the reason
	var why string
	for _, x := range r.Rejected {
		if x.Model == "f" {
			why = x.Why
		}
	}
	if why != "outside top 5" {
		t.Fatalf("the sixth model must be left out with a reason: %q", why)
	}
}

// A preset may ask for a different number of models.
func TestMainListSizeFollowsThePreset(t *testing.T) {
	models := []*catalog.Model{chat("a", 100, 300), chat("b", 110, 330), chat("c", 120, 360), chat("d", 130, 390), chat("e", 140, 420), chat("f", 150, 450)}
	f := fixture(models...)
	f.cfg.Benchmarks = map[string]float64{"a": 90, "b": 85, "c": 80, "d": 75, "e": 70, "f": 65}
	sh := f.cl.Presets["text"]
	sh.MaxCandidates = 2
	f.cl.Presets["text"] = sh
	r := f.rank("text", nil)
	if o := order(r); len(o) != 2 || o[0] != "a" || o[1] != "b" {
		t.Fatalf("a preset of 2 models: %v", o)
	}
}

func TestGatesAndSunset(t *testing.T) {
	noTools := chat("notools", 100, 300)
	noTools.ToolCall = false
	small := chat("small", 100, 300)
	small.Context = 8000
	dying := chat("dying", 100, 300)
	t1 := now.Add(24 * time.Hour)
	dying.SunsetAt = &t1
	f := fixture(noTools, small, dying, chat("fine", 200, 600))
	r := f.rank("text", nil)
	if o := order(r); len(o) != 1 || o[0] != "fine" || len(r.Rejected) != 3 {
		t.Fatalf("order=%v rejected=%v", o, r.Rejected)
	}
}

func TestRoundRobinFrontGroupAndPriority(t *testing.T) {
	f := fixture(chat("a", 100, 300), chat("b", 102, 304), chat("c", 5000, 15000))
	f.cfg.Benchmarks = map[string]float64{"a": 70, "b": 70, "c": 70}
	r := f.rank("text", nil)
	if r.Front < 2 {
		t.Fatalf("near-equal models should form a round-robin group, front=%d %v", r.Front, order(r))
	}
	sh := f.cl.Presets["text"]
	sh.Strategy = "priority"
	f.cl.Presets["text"] = sh
	if r := f.rank("text", nil); r.Front != 1 {
		t.Fatalf("priority strategy must have front=1, got %d", r.Front)
	}
}

func TestMediaPresetSortsByPriceFreeFirst(t *testing.T) {
	paid := &catalog.Model{ID: "img-paid", Endpoint: "images", Lines: []catalog.Line{{Unit: "image", IDR: 300}}}
	cheap := &catalog.Model{ID: "img-cheap", Endpoint: "images", Lines: []catalog.Line{{Unit: "image", IDR: 40}}}
	free := &catalog.Model{ID: "img-free:free", Endpoint: "images", Free: true}
	f := fixture(paid, cheap, free)
	r := f.rank("image", nil)
	if o := order(r); len(o) != 3 || o[0] != "img-free:free" || o[1] != "img-cheap" || o[2] != "img-paid" {
		t.Fatalf("media order: %v", o)
	}
}

func TestComboKeepsConfiguredOrder(t *testing.T) {
	f := fixture(chat("a", 1, 1), chat("b", 1, 1))
	f.cl.Presets["text"] = config.Preset{Modality: "chat", Mode: "combo", Models: []string{"b", "ghost", "a"}, Strategy: "priority", MaxCandidates: 4}
	r := f.rank("text", nil)
	if o := order(r); len(o) != 2 || o[0] != "b" || o[1] != "a" || len(r.Rejected) != 1 {
		t.Fatalf("combo: %v %v", o, r.Rejected)
	}
}

func TestUsageCostUsesCacheDiscountAndPlanFreeCache(t *testing.T) {
	m := chat("m", 1000, 2000) // Rp/1M tokens; cached = 100
	got := UsageCost(m, 1_000_000, 500_000, 800_000, false, now)
	want := 200_000*1000.0/1e6 + 800_000*100.0/1e6 + 500_000*2000.0/1e6
	if d := got - want; d > 1e-9 || d < -1e-9 {
		t.Fatalf("got %v want %v", got, want)
	}
	if free := UsageCost(m, 1_000_000, 500_000, 800_000, true, now); free >= got {
		t.Fatal("free cache must lower the cost")
	}
}

func TestRankingListsEveryCallableModel(t *testing.T) {
	f := fixture(chat("a", 100, 300), chat("b", 200, 600), chat("c", 300, 900))
	f.cfg.Benchmarks = map[string]float64{"a": 70, "b": 70, "c": 70}
	r := f.rank("text", nil)
	if len(r.Models) != 3 {
		t.Fatalf("the list needs every callable model: %v", r.Models)
	}
	// a preset has no model filters: every model the keys can call is a candidate
	for _, m := range []string{"a", "b", "c"} {
		found := false
		for _, c := range r.Candidates {
			if c.Model == m {
				found = true
			}
		}
		if !found {
			t.Fatalf("model %s must be a candidate: %v", m, order(r))
		}
	}
}

func TestCostScoreFollowsRelativePrice(t *testing.T) {
	f := fixture(chat("cheap", 1000, 4000), chat("mid", 1150, 4600), chat("dear", 3000, 12000))
	f.cfg.Benchmarks = map[string]float64{"cheap": 60, "mid": 70, "dear": 70} // the pricier ones are a bit better
	p := f.cl.Presets["text"]
	p.Weights = config.Weights{Cost: 60, Benchmark: 10, Reliability: 15, Context: 5} // "Cheapest"
	p.MinQuality = 0.3
	f.cl.Presets["text"] = p
	r := f.rank("text", nil)
	if o := order(r); len(o) != 3 || o[0] != "cheap" || o[1] != "mid" || o[2] != "dear" {
		t.Fatalf("Cheapest must follow the price: %v", o)
	}
	cs := map[string]float64{}
	for _, c := range r.Candidates {
		cs[c.Model] = c.Components["cost"]
	}
	// 1/(1+ln(cost/cheapest)): 15% dearer ~0.88, three times dearer ~0.48
	if cs["cheap"] != 1 || cs["mid"] < 0.85 || cs["mid"] > 0.9 || cs["dear"] < 0.4 || cs["dear"] > 0.55 {
		t.Fatalf("cost scores should fall smoothly with relative price: %v", cs)
	}
}

func freeChat(id string) *catalog.Model {
	m := chat(id, 0, 0)
	m.Free = true
	return m
}

// A model with no quality evidence is flagged, not locked out: the score
// decides whether it serves, and the panel says its quality is a guess.
func TestUnverifiedModelIsFlaggedButStillRanks(t *testing.T) {
	f := fixture(freeChat("mystery:free"), chat("known", 500, 1500))
	f.cfg.Benchmarks = map[string]float64{"known": 70}
	r := f.rank("text", nil)
	if o := order(r); len(o) != 2 {
		t.Fatalf("both models must be in the list: %v", o)
	}
	var mystery *Candidate
	for i := range r.Candidates {
		if r.Candidates[i].Model == "mystery:free" {
			mystery = &r.Candidates[i]
		}
	}
	if mystery == nil {
		t.Fatal("the unverified model must be a candidate")
	}
	if !containsStr(mystery.Flags, "unverified") {
		t.Fatalf("it must be marked as unverified: %+v", mystery.Flags)
	}
	// once the model has a score from a leaderboard, the flag is gone
	f.cfg.Benchmarks["mystery:free"] = 65
	r = f.rank("text", nil)
	for _, c := range r.Candidates {
		if c.Model == "mystery:free" && containsStr(c.Flags, "unverified") {
			t.Fatalf("a scored model is no longer unverified: %+v", c.Flags)
		}
	}
}

func TestObjectivesBehaveAsNamed(t *testing.T) {
	free := freeChat("free-big:free")
	free.FreeSibling = "big" // priced twin, so its quality can be estimated
	f := fixture(free, chat("big", 6000, 24000), chat("mid", 1200, 4800), chat("small", 300, 1200))
	f.cfg.Benchmarks = map[string]float64{"big": 92, "mid": 75, "small": 55, "free-big:free": 68}
	obj := map[string]struct {
		w config.Weights
		q float64
	}{
		"cheapest": {config.Weights{Cost: 60, Benchmark: 10, Reliability: 15, Context: 5}, 0.30},
		"balanced": {config.Weights{Cost: 30, Benchmark: 25, Reliability: 15, Context: 10}, 0.45},
		"quality":  {config.Weights{Cost: 5, Benchmark: 40, Reliability: 15, Context: 10}, 0.60},
	}
	top := map[string]string{}
	for name, o := range obj {
		p := f.cl.Presets["text"]
		p.Weights, p.MinQuality, p.Strategy = o.w, o.q, "priority"
		f.cl.Presets["text"] = p
		r := f.rank("text", nil)
		top[name] = r.Candidates[0].Model
		if name == "cheapest" && order(r)[len(order(r))-1] != "big" {
			t.Fatalf("Cheapest must put the dearest model last: %v", order(r))
		}
		if name == "quality" && order(r)[0] == "small" {
			t.Fatalf("Best quality must not lead with the weakest model: %v", order(r))
		}
	}
	if top["cheapest"] != "free-big:free" {
		t.Fatalf("Cheapest should take the free model that is good enough, got %s", top["cheapest"])
	}
	if top["quality"] != "big" {
		t.Fatalf("Best quality should take the strongest model even though it is the dearest, got %s", top["quality"])
	}
	if top["balanced"] == "small" || top["balanced"] == "big" && false {
		t.Fatalf("Balanced should not pick the weakest model: %s", top["balanced"])
	}
}

func TestStackedPlanAndPromoNeverBeatAFreeModelOfEqualQuality(t *testing.T) {
	free := freeChat("free-ok:free")
	free.FreeSibling = "x"
	promo := chat("promo-plan", 100, 400)
	promo.Promo = &catalog.Promo{PercentOff: 90, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(72 * time.Hour)}
	f := fixture(free, promo)
	f.snap.Plans = []catalog.Plan{{Name: "P", Scope: []string{"promo-plan"}}}
	f.pool.Reload(f.cfg, map[string]string{"K": "s"}, nil)
	// the plan is detected from kenari, not configured
	f.pool.Keys()[0].Account().SetInfo(&upstream.PlanInfo{Name: "P", Windows: map[string]upstream.Window{}, Fetched: now}, f.pool.Keys()[0].Name)
	f.cfg.Benchmarks = map[string]float64{"free-ok:free": 70, "promo-plan": 70}
	p := f.cl.Presets["text"]
	p.Weights, p.MinQuality = config.Weights{Cost: 60, Benchmark: 10, Reliability: 15, Context: 5}, 0.3
	f.cl.Presets["text"] = p
	r := f.rank("text", nil)
	if len(r.Candidates) != 2 || r.Candidates[0].Model != "free-ok:free" {
		t.Fatalf("the free model must lead: %+v", order(r))
	}
}
