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
	cfg.Clients = []config.Client{{Name: "c", Enabled: true, Presets: config.DefaultPresets(), Keys: []config.Upstream{{Name: "k", KeyEnv: "K", Enabled: true, Tier: "topup"}}}}
	pool := upstream.NewPool()
	pool.Reload(cfg, map[string]string{"K": "s"}, nil)
	snap := &catalog.Snapshot{Models: map[string]*catalog.Model{}, Tier: catalog.FreeTier{Free: catalog.TierLimit{RPM: 5, Daily: 50}, Topup: catalog.TierLimit{RPM: 10, Daily: 1000}, Plan: catalog.TierLimit{RPM: 25}}}
	for _, m := range models {
		snap.Models[m.ID] = m
	}
	return &fx{snap, cfg, pool, &cfg.Clients[0]}
}

func (f *fx) rank(preset string, mod func(*Inputs)) *Ranking {
	in := Inputs{Cfg: f.cfg, Snap: f.snap, Pool: f.pool, Now: now, Seen: map[string]time.Time{}, JEV: map[string]store.JEVScore{}, Stats: map[string]store.ModelStat{}, Life: map[string]store.ModelStat{}}
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

func TestNewModelsGoToTrialUntilProven(t *testing.T) {
	f := fixture(chat("old", 500, 1500), chat("shiny", 400, 1200))
	f.cfg.Benchmarks = map[string]float64{"old": 70, "shiny": 70}
	seen := map[string]time.Time{"old": time.Unix(0, 0), "shiny": now.Add(-2 * 24 * time.Hour)}
	r := f.rank("text", func(in *Inputs) { in.Seen = seen })
	if len(r.Trial) != 1 || r.Trial[0].Model != "shiny" || len(r.Candidates) != 1 {
		t.Fatalf("trial=%v main=%v", r.Trial, order(r))
	}
	r = f.rank("text", func(in *Inputs) {
		in.Seen = seen
		in.Life = map[string]store.ModelStat{"shiny": {N: 40, OK: 39}}
	})
	if len(r.Trial) != 0 || len(r.Candidates) != 2 {
		t.Fatalf("proven model should join the main list: trial=%v main=%v", r.Trial, order(r))
	}
}

func TestTrialCostGuard(t *testing.T) {
	f := fixture(chat("old", 100, 300), chat("lavish", 20000, 60000))
	f.cfg.Benchmarks = map[string]float64{"old": 70, "lavish": 70}
	r := f.rank("text", func(in *Inputs) {
		in.Seen = map[string]time.Time{"old": time.Unix(0, 0), "lavish": now.Add(-time.Hour)}
	})
	if len(r.Trial) != 0 {
		t.Fatal("a new model costing far more than the leader must not get canary traffic")
	}
}

func TestJEVThresholdRejectsAndScoreLifts(t *testing.T) {
	f := fixture(chat("a", 500, 1500), chat("b", 500, 1500))
	f.cfg.JEV.Enabled = true
	r := f.rank("text", func(in *Inputs) {
		in.JEV = map[string]store.JEVScore{"a": {Score: 0.95}, "b": {Score: 0.2}}
	})
	if o := order(r); len(o) != 1 || o[0] != "a" {
		t.Fatalf("b should fail the JEV threshold: %v %v", o, r.Rejected)
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
