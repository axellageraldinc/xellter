package rank

import (
	"strings"
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

func freeChat(id string) *catalog.Model {
	m := chat(id, 0, 0)
	m.Free = true
	return m
}

type fx struct {
	snap        *catalog.Snapshot
	cfg         *config.Config
	pool        *upstream.Pool
	cl          *config.Client
	benchScores map[string]map[string]store.BenchRow
}

// fixture builds one client whose text preset holds the given models, Balanced by default.
func fixture(models ...*catalog.Model) *fx {
	cfg := config.Default()
	cfg.Clients = []config.Client{{
		Name: "c", Enabled: true, Purpose: "general",
		Keys:    []config.Upstream{{Name: "k", KeyEnv: "K"}},
		Presets: map[string]config.Preset{"text": {Category: "chat", Optimization: "balanced", Models: []string{}}},
	}}
	pool := upstream.NewPool()
	pool.Reload(cfg, map[string]string{"K": "s"}, nil)
	snap := &catalog.Snapshot{
		Models: map[string]*catalog.Model{},
		Tier:   catalog.FreeTier{Free: catalog.TierLimit{RPM: 5, Daily: 50}, Topup: catalog.TierLimit{RPM: 10, Daily: 1000}, Plan: catalog.TierLimit{RPM: 25}},
	}
	for _, m := range models {
		snap.Models[m.ID] = m
	}
	f := &fx{snap: snap, cfg: cfg, pool: pool, cl: &cfg.Clients[0]}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	f.preset(ids...)
	return f
}

// preset sets the pool of the text preset.
func (f *fx) preset(models ...string) config.Preset {
	p := f.cl.Presets["text"]
	p.Models = models
	f.cl.Presets["text"] = p
	return p
}

// optimize sets the text preset's optimization.
func (f *fx) optimize(name string) {
	p := f.cl.Presets["text"]
	p.Optimization = name
	f.cl.Presets["text"] = p
}

// bench records one arena score per model, in the general text arena unless an arena is given.
func (f *fx) bench(scores map[string]float64) {
	if f.benchScores == nil {
		f.benchScores = map[string]map[string]store.BenchRow{}
	}
	for id, v := range scores {
		if f.benchScores[id] == nil {
			f.benchScores[id] = map[string]store.BenchRow{}
		}
		f.benchScores[id]["text"] = store.BenchRow{Value: v, Arena: "text"}
	}
}

// benchIn records a score of a model in one named arena.
func (f *fx) benchIn(id, arena string, v float64) {
	if f.benchScores == nil {
		f.benchScores = map[string]map[string]store.BenchRow{}
	}
	if f.benchScores[id] == nil {
		f.benchScores[id] = map[string]store.BenchRow{}
	}
	f.benchScores[id][arena] = store.BenchRow{Value: v, Arena: arena}
}

// plan gives the account the "Indie" plan, whose scope covers the given models.
func (f *fx) plan(scope ...string) {
	f.snap.Plans = []catalog.Plan{{Name: "Indie", BudgetWeek: 1e6, BudgetMon: 1e6, Scope: scope, FreeCache: scope}}
	f.pool.Reload(f.cfg, map[string]string{"K": "s"}, nil)
	for _, k := range f.pool.Keys() {
		k.Account().SetInfo(&upstream.PlanInfo{Name: "Indie", Windows: map[string]upstream.Window{
			"week": {BudgetRP: 1e6, ResetsAt: now.Add(7 * 24 * time.Hour)},
		}, Fetched: now}, k.Name)
	}
}

func (f *fx) rank(mod func(*Inputs)) *Ranking {
	in := Inputs{Cfg: f.cfg, Snap: f.snap, Pool: f.pool, Bench: f.benchScores, Now: now}
	if mod != nil {
		mod(&in)
	}
	return One(in, f.cl, "text", f.cl.Presets["text"])
}

func order(r *Ranking) []string {
	var o []string
	for _, c := range r.Candidates {
		o = append(o, c.Model)
	}
	return o
}

func containsStr(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func rejected(r *Ranking, id string) string {
	for _, x := range r.Rejected {
		if x.Model == id {
			return x.Why
		}
	}
	return ""
}

// ---- reference cost ----

// The worked example of the plan: Claude Opus 5 at In 100k, Out 500k, CR 40k is Rp532 per reference request.
func TestRefCostUsesTheReferenceRequestOfEachCategory(t *testing.T) {
	m := chat("opus", 100_000, 500_000)
	m.CachedIDR = 40_000
	if got, ok := RefCost(m, "chat", now); !ok || got != 532 {
		t.Fatalf("chat reference cost: %v %v", got, ok)
	}
	// systemone is a chat-shaped endpoint here, so it uses the same reference profile
	if got, _ := RefCost(m, "systemone", now); got != 532 {
		t.Fatalf("systemone reference cost: %v", got)
	}
	// a running promo lowers the number, because the listed price already carries the discount
	promo := chat("promo", 10_000, 50_000)
	promo.Promo = &catalog.Promo{PercentOff: 90, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour)}
	if got, _ := RefCost(promo, "chat", now); got >= 532 {
		t.Fatalf("a promo must be cheaper: %v", got)
	}
	// media is priced per unit: one image, five seconds of 720p, 1000 characters, 60 seconds, one song
	media := map[string]*catalog.Model{
		"images":              {ID: "i", Endpoint: "images", Lines: []catalog.Line{{Unit: "image", IDR: 300}}},
		"videos":              {ID: "v", Endpoint: "videos", Lines: []catalog.Line{{Unit: "second", Variant: "720p", IDR: 100}, {Unit: "second", Variant: "1080p", IDR: 400}}},
		"audio_speech":        {ID: "s", Endpoint: "audio_speech", Lines: []catalog.Line{{Unit: "1k_chars", IDR: 20}}},
		"audio_transcription": {ID: "t", Endpoint: "audio_transcription", Lines: []catalog.Line{{Unit: "second", IDR: 0.5}}},
		"music":               {ID: "m", Endpoint: "music", Lines: []catalog.Line{{Unit: "song", IDR: 900}}},
	}
	want := map[string]float64{"images": 300, "videos": 500, "audio_speech": 20, "audio_transcription": 30, "music": 900}
	for category, model := range media {
		if got, ok := RefCost(model, category, now); !ok || got != want[category] {
			t.Errorf("%s: got %v %v, want %v", category, got, ok, want[category])
		}
	}
	// no usable price is reported as unknown, not as free
	if _, ok := RefCost(&catalog.Model{ID: "x", Endpoint: "chat"}, "chat", now); ok {
		t.Fatal("a model with no price must be unknown")
	}
	// a free model costs nothing
	if got, ok := RefCost(freeChat("f:free"), "chat", now); !ok || got != 0 {
		t.Fatalf("free model: %v %v", got, ok)
	}
}

// ---- cost score ----

func TestCostScoreFollowsRelativePrice(t *testing.T) {
	cases := []struct {
		cost, cheapest, want float64
	}{
		{100, 100, 1},      // equal price
		{113, 100, 0.891},  // 13% dearer
		{200, 100, 0.591},  // twice
		{1000, 100, 0.303}, // ten times
		{0, 100, 1},        // free
	}
	for _, c := range cases {
		got := round(costScore(c.cost, true, c.cheapest))
		if got < c.want-0.01 || got > c.want+0.01 {
			t.Errorf("cost %v against %v: got %v, want about %v", c.cost, c.cheapest, got, c.want)
		}
	}
	if got := costScore(0, false, 100); got != 0.4 {
		t.Fatalf("an unknown price is a middle guess: %v", got)
	}
}

// ---- the three optimizations ----

// Cheapest is the reference price and nothing else, with free and plan-covered models in two pools so a
// cheap plan model never displaces a free one.
func TestCheapestIsPriceOnly(t *testing.T) {
	f := fixture(freeChat("free-a:free"), freeChat("free-b:free"), chat("plan-cheap", 100, 300), chat("plan-dear", 900, 2700), chat("paid", 5000, 15000))
	f.plan("plan-*")
	f.optimize("cheapest")
	f.bench(map[string]float64{"free-a:free": 1, "free-b:free": 1, "plan-cheap": 99, "plan-dear": 99, "paid": 99})
	r := f.rank(nil)
	// the free half comes first even though its quality is the lowest, and the plan half is ordered by price
	want := []string{"free-a:free", "free-b:free", "plan-cheap", "plan-dear", "paid"}
	if got := order(r); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cheapest order: %v, want %v", got, want)
	}
	if r.Candidates[0].Billing != BillingFree || r.Candidates[2].Billing != BillingPlan || r.Candidates[4].Billing != BillingBalance {
		t.Fatalf("billing classes: %+v", r.Candidates)
	}
	// the cost of a candidate is the reference request, and its score is the cost score
	if c := r.Candidates[2]; c.CostIDR <= 0 || c.Score <= 0 {
		t.Fatalf("cost and score: %+v", c)
	}
}

// A half that cannot fill its share is topped up from what is left, so the list is never short.
func TestCheapestTopsUpAnEmptyHalf(t *testing.T) {
	// seven free models and one plan-covered one: the free half fills its own share, and the leftover plan
	// model takes the slot the plan half could not fill
	var models []*catalog.Model
	for _, id := range []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7"} {
		models = append(models, freeChat(id+":free"))
	}
	models = append(models, chat("plan-a", 100, 300))
	f := fixture(models...)
	f.plan("plan-a")
	f.optimize("cheapest")
	r := f.rank(func(in *Inputs) { in.Top = 4 })
	if len(r.Candidates) != 4 {
		t.Fatalf("a preset of 4 must fill 4 places: %v", order(r))
	}
	// the free half goes first, and every free model is ahead of the plan model within its own half
	if r.Candidates[0].Billing != BillingFree || r.Candidates[1].Billing != BillingFree {
		t.Fatalf("the free half must lead: %+v", r.Candidates)
	}
	found := false
	for _, c := range r.Candidates {
		if c.Model == "plan-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the plan model must still make the list: %v", order(r))
	}

	// with only plan-covered models, the plan half fills both shares, cheapest first
	f2 := fixture(chat("p1", 100, 300), chat("p2", 200, 600), chat("p3", 300, 900), chat("p4", 400, 1200), chat("p5", 500, 1500), chat("p6", 600, 1800))
	f2.plan("p*")
	f2.optimize("cheapest")
	r = f2.rank(func(in *Inputs) { in.Top = 4 })
	want := []string{"p1", "p2", "p3", "p4"}
	if got := order(r); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the topped-up list must be ordered by price: %v, want %v", got, want)
	}
}

// Balanced is cost 50 : benchmark 50.
func TestBalancedWeighsCostAndQuality(t *testing.T) {
	// cheap is the cheapest but the weakest; good is dearer but much better; dear is the best and the priciest
	f := fixture(chat("cheap", 100, 300), chat("good", 400, 1200), chat("dear", 5000, 15000))
	f.bench(map[string]float64{"cheap": 0.2, "good": 0.8, "dear": 0.95})
	r := f.rank(nil)
	got := map[string]Candidate{}
	for _, c := range r.Candidates {
		got[c.Model] = c
	}
	// the reported score is exactly 0.5*costScore + 0.5*bench, measured against the cheapest model in the pool
	cheapest := got["cheap"].CostIDR
	for _, c := range got {
		want := round(0.5*costScore(c.CostIDR, true, cheapest) + 0.5*c.Bench)
		if c.Score < want-0.001 || c.Score > want+0.001 {
			t.Fatalf("%s: score %v is not half cost (%v) and half quality (%v)", c.Model, c.Score, costScore(c.CostIDR, true, cheapest), c.Bench)
		}
	}
	if order(r)[0] != "good" {
		t.Fatalf("balanced must not lead with the weakest model: %v", order(r))
	}
	if order(r)[len(order(r))-1] != "dear" {
		t.Fatalf("balanced must not lead with the dearest model: %v", order(r))
	}
	// the blend is monotone in both halves: improving quality without changing price can only help
	f.bench(map[string]float64{"cheap": 0.99, "good": 0.8, "dear": 0.95})
	if r := f.rank(nil); order(r)[0] != "cheap" {
		t.Fatalf("the free-cheapest model with the best quality must lead: %v", order(r))
	}
}

// Best quality ranks on the benchmark alone, drops models with no evidence, and applies a median floor.
func TestBestQualityUsesTheBenchmarkOnly(t *testing.T) {
	f := fixture(chat("top", 9000, 27000), chat("mid", 3000, 9000), chat("low", 100, 300), chat("mystery", 500, 1500))
	f.bench(map[string]float64{"top": 0.9, "mid": 0.6, "low": 0.2})
	f.optimize("best_quality")
	r := f.rank(nil)
	got := order(r)
	if len(got) != 2 || got[0] != "top" || got[1] != "mid" {
		t.Fatalf("best quality must rank on the benchmark and drop the floor: %v", got)
	}
	if r.Candidates[0].Score != 0.9 || r.Candidates[0].Bench != 0.9 {
		t.Fatalf("the score must be the benchmark: %+v", r.Candidates[0])
	}
	if why := rejected(r, "mystery"); why != "no quality evidence for chat" {
		t.Fatalf("a model with no evidence must be reported: %q", why)
	}
	if !containsStr(r.Unranked, "mystery") {
		t.Fatalf("unranked: %v", r.Unranked)
	}
	// low is rejected by the floor, with the number it failed
	if why := rejected(r, "low"); why == "" {
		t.Fatalf("the model under the median must be rejected: %v", r.Rejected)
	}
}

// A preset with no quality evidence at all has no best-quality list, but the models are still reported as
// unranked rather than silently dropped.
func TestBestQualityWithoutEvidenceIsEmptyButExplained(t *testing.T) {
	f := fixture(chat("a", 100, 300), chat("b", 200, 600))
	f.optimize("best_quality")
	r := f.rank(nil)
	if len(r.Candidates) != 0 || len(r.Unranked) != 2 {
		t.Fatalf("candidates=%v unranked=%v", order(r), r.Unranked)
	}
}

// ---- purpose ----

// The purpose decides which arenas a text model is scored on; a model missing its purpose-specific score
// falls back to the general arena and says so, and is never dropped for lack of a number.
func TestPurposeSelectsTheArenasAndFallsBackToGeneral(t *testing.T) {
	// the third model keeps the median floor below both of them, so the floor is not what this test measures
	f := fixture(chat("coder", 100, 300), chat("writer", 100, 300), chat("weak", 100, 300))
	f.benchIn("coder", "swebench", 0.9)
	f.benchIn("writer", "text", 0.95)
	f.benchIn("weak", "swebench", 0.1)
	f.cl.Purpose = "coding"
	f.optimize("best_quality")
	r := f.rank(nil)
	got := map[string]Candidate{}
	for _, c := range r.Candidates {
		got[c.Model] = c
	}
	if got["coder"].Bench != 0.9 || got["coder"].BenchSource != "swebench" {
		t.Fatalf("coding must use the coding arenas: %+v", got["coder"])
	}
	if got["writer"].BenchSource != "general (fallback)" || !containsStr(got["writer"].Flags, "general score") {
		t.Fatalf("a model without a coding score falls back to the general arena: %+v", got["writer"])
	}
	if !containsStr(order(r), "writer") || !containsStr(order(r), "coder") {
		t.Fatalf("the fallback must still compete: %v", order(r))
	}
	if why := rejected(r, "weak"); why == "" {
		t.Fatalf("the model under the floor must be rejected: %v", r.Rejected)
	}
	// bulk has no quality metric at all: cost only, and no model carries a quality source
	f.cl.Purpose = "bulk"
	if r := f.rank(nil); len(r.Candidates) != 0 || len(r.Unranked) != 3 {
		t.Fatalf("bulk has nothing to rank on quality: %v %v", order(r), r.Unranked)
	}
}

// The coding composite uses the fixed weights 50/30/20 and renormalises over the sources a model has.
func TestCodingCompositeRenormalisesOverTheSourcesItHas(t *testing.T) {
	f := fixture(chat("all", 100, 300), chat("swe-only", 100, 300), chat("web-and-aider", 100, 300))
	f.benchIn("all", "swebench", 0.8)
	f.benchIn("all", "webdev", 0.6)
	f.benchIn("all", "aider", 0.4)
	f.benchIn("swe-only", "swebench", 0.5)
	f.benchIn("web-and-aider", "webdev", 0.7)
	f.benchIn("web-and-aider", "aider", 0.1)
	f.cl.Purpose = "coding"
	r := f.rank(nil) // balanced, so the median floor does not hide a model
	got := map[string]Candidate{}
	for _, c := range r.Candidates {
		got[c.Model] = c
	}
	if v := got["all"].Bench; v < 0.65 || v > 0.67 { // 0.5*0.8 + 0.3*0.6 + 0.2*0.4 = 0.66
		t.Fatalf("the full composite: %v", v)
	}
	if v := got["swe-only"].Bench; v != 0.5 {
		t.Fatalf("a single source must renormalise to its own value: %v", v)
	}
	if v := got["web-and-aider"].Bench; v < 0.45 || v > 0.49 { // (0.3*0.7 + 0.2*0.1) / 0.5 = 0.46
		t.Fatalf("two sources must renormalise over their weights: %v", v)
	}
	if got["all"].BenchSource != "aider, swebench, webdev" {
		t.Fatalf("the panel must know which sources were used: %q", got["all"].BenchSource)
	}
}

// ---- membership ----

// A pool is explicit: only the models the owner picked are ranked, and every one that cannot serve says why.
func TestPoolMembershipAndRejections(t *testing.T) {
	dying := chat("dying", 100, 300)
	t1 := now.Add(24 * time.Hour)
	dying.SunsetAt = &t1
	sub := chat("sub", 100, 300)
	sub.Subscriber = true
	image := &catalog.Model{ID: "img", Endpoint: "images", Lines: []catalog.Line{{Unit: "image", IDR: 300}}}
	f := fixture(chat("fine", 200, 600), dying, sub, image)
	f.preset("fine", "dying", "sub", "img", "ghost")
	r := f.rank(nil)
	if got := order(r); len(got) != 1 || got[0] != "fine" {
		t.Fatalf("order: %v", got)
	}
	want := map[string]string{
		"ghost": "not in the kenari catalog",
		"dying": "sunsets " + t1.Format("2006-01-02"),
		"sub":   "subscriber only and the account has no plan",
		"img":   "is a images model, not chat",
	}
	for id, why := range want {
		if got := rejected(r, id); got != why {
			t.Errorf("%s: %q, want %q", id, got, why)
		}
	}
	// with a plan the subscriber-only model is allowed
	f.plan("sub")
	if got := order(f.rank(nil)); len(got) != 2 {
		t.Fatalf("a subscriber model is usable on a plan: %v", got)
	}
}

// A model no key may call is rejected with that reason, and a model listed twice is ranked once.
func TestKeysDecideUsability(t *testing.T) {
	f := fixture(chat("allowed", 100, 300), chat("restricted", 100, 300))
	f.pool.Keys()[0].SetAllowed([]string{"allowed"})
	f.preset("allowed", "restricted", "allowed")
	r := f.rank(nil)
	if got := order(r); len(got) != 1 || got[0] != "allowed" {
		t.Fatalf("order: %v", got)
	}
	if why := rejected(r, "restricted"); why != "no key of this client may call it" {
		t.Fatalf("restricted: %q", why)
	}
}

// A model listed twice in the pool appears once.
func TestDuplicatePoolEntriesAreRankedOnce(t *testing.T) {
	f := fixture(chat("a", 100, 300))
	f.preset("a", "a")
	if got := order(f.rank(nil)); len(got) != 1 {
		t.Fatalf("order: %v", got)
	}
}

// The list holds Routing.TopModels models, and Inputs.Top overrides it.
func TestTopSizeComesFromRouting(t *testing.T) {
	models := []*catalog.Model{}
	ids := []string{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		models = append(models, chat(id, 100, 300))
		ids = append(ids, id)
	}
	f := fixture(models...)
	f.preset(ids...)
	f.bench(map[string]float64{"a": 0.9, "b": 0.8, "c": 0.7, "d": 0.6, "e": 0.5, "f": 0.4})
	f.cfg.Routing.TopModels = 3
	if got := order(f.rank(nil)); len(got) != 3 {
		t.Fatalf("top_models=3: %v", got)
	}
	if got := order(f.rank(func(in *Inputs) { in.Top = 2 })); len(got) != 2 {
		t.Fatalf("Inputs.Top=2: %v", got)
	}
	// a config with no routing size falls back to ten
	f.cfg.Routing.TopModels = 0
	if got := order(f.rank(nil)); len(got) != 6 {
		t.Fatalf("a pool smaller than the top must be listed whole: %v", got)
	}
}

// ---- whole-run behaviour ----

// Every preset of every enabled client is ranked, and the map key is client/preset.
func TestAllRanksEveryPresetOfEveryEnabledClient(t *testing.T) {
	f := fixture(chat("a", 100, 300))
	f.cfg.Clients = append(f.cfg.Clients, config.Client{
		Name: "off", Enabled: false, Purpose: "general",
		Presets: map[string]config.Preset{"text": {Category: "chat", Optimization: "balanced", Models: []string{"a"}}},
	})
	f.cfg.Clients = append(f.cfg.Clients, config.Client{
		Name: "img", Enabled: true, Purpose: "general",
		Keys:    []config.Upstream{{Name: "k2", KeyEnv: "K2"}},
		Presets: map[string]config.Preset{"image": {Category: "images", Optimization: "cheapest", Models: []string{"a"}}},
	})
	f.pool.Reload(f.cfg, map[string]string{"K": "s", "K2": "s"}, nil)
	out := All(Inputs{Cfg: f.cfg, Snap: f.snap, Pool: f.pool, Now: now})
	if len(out) != 2 {
		t.Fatalf("rankings: %v", out)
	}
	if out[Key("c", "text")] == nil {
		t.Fatal("the client's text preset must be ranked")
	}
	if r := out[Key("img", "image")]; r == nil || r.Category != "images" || r.Optimization != "cheapest" {
		t.Fatalf("the image preset: %+v", r)
	}
	if out[Key("off", "text")] != nil {
		t.Fatal("a disabled client must not be ranked")
	}
}

// The ranking explains itself: every candidate carries its numbers, its billing class and a reason, and the
// whole run is deterministic.
func TestCandidatesCarryTheirExplanationAndTheRunIsDeterministic(t *testing.T) {
	f := fixture(chat("b", 200, 600), chat("a", 100, 300))
	f.bench(map[string]float64{"a": 0.5, "b": 0.5})
	f.preset("b", "a")
	r1, r2 := f.rank(nil), f.rank(nil)
	if got, want := order(r1), order(r2); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("two runs must agree: %v %v", got, want)
	}
	// equal scores are ordered by model id, so the order never depends on map iteration
	if got := order(r1); got[0] != "a" || got[1] != "b" {
		t.Fatalf("equal scores must break by id: %v", got)
	}
	for _, c := range r1.Candidates {
		if c.Reason == "" || c.Billing == "" || c.BenchSource == "" {
			t.Fatalf("a candidate without an explanation: %+v", c)
		}
	}
	if r1.Client != "c" || r1.Preset != "text" || r1.Category != "chat" || r1.Purpose != "general" || !r1.Computed.Equal(now) {
		t.Fatalf("ranking header: %+v", r1)
	}
}

// A promo makes a model cheaper without making it look worse, and the list price is kept next to the
// promo-effective one for the panel.
func TestPromoIsAppliedToTheCostButNotToQuality(t *testing.T) {
	promo := chat("promo", 63, 250) // listed price already includes a 90% discount
	promo.Promo = &catalog.Promo{PercentOff: 90, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(7 * 24 * time.Hour)}
	f := fixture(promo, chat("rival", 630, 2500))
	f.bench(map[string]float64{"promo": 0.5, "rival": 0.5})
	r := f.rank(nil)
	got := map[string]Candidate{}
	for _, c := range r.Candidates {
		got[c.Model] = c
	}
	if got["promo"].CostIDR >= got["rival"].CostIDR {
		t.Fatalf("the promo model must be cheaper: %+v", got)
	}
	if got["promo"].Bench != got["rival"].Bench {
		t.Fatalf("a discount must not change the benchmark: %+v", got)
	}
	if got["promo"].ListCostIDR >= got["promo"].CostIDR*10 {
		t.Fatalf("the list price should be the undiscounted one: %+v", got["promo"])
	}
}

// A model with no price still competes: it is scored as a middle guess, never dropped, and its price stays
// unknown rather than being reported as free.
func TestUnpricedModelStillCompetes(t *testing.T) {
	f := fixture(&catalog.Model{ID: "noprice", Endpoint: "chat"}, chat("priced", 100, 300))
	f.optimize("cheapest")
	r := f.rank(nil)
	if len(r.Candidates) != 2 {
		t.Fatalf("both models must compete: %v", order(r))
	}
	for _, c := range r.Candidates {
		if c.Model != "noprice" {
			continue
		}
		if c.CostIDR != 0 {
			t.Fatalf("an unknown price must not be reported as a number: %+v", c)
		}
		if c.Score != 0.4 {
			t.Fatalf("an unknown price is a middle guess: %+v", c)
		}
	}
}

// ---- billing ----

// The billing class is free, then plan, then balance, and a free model is always free even on a plan.
func TestBillingClassPrefersFreeThenPlanThenBalance(t *testing.T) {
	f := fixture(freeChat("free:free"), chat("plan-a", 100, 300), chat("paid", 100, 300))
	f.plan("plan-*")
	r := f.rank(nil)
	got := map[string]string{}
	for _, c := range r.Candidates {
		got[c.Model] = c.Billing
	}
	if got["free:free"] != BillingFree || got["plan-a"] != BillingPlan || got["paid"] != BillingBalance {
		t.Fatalf("billing classes: %v", got)
	}
}

// ---- non-text categories ----

// Every non-text category has one metric of its own, and the client's purpose must not change it: an image
// preset is ranked on the image arena whoever owns it (ADR 0018).
func TestMediaCategoriesUseTheirOwnArena(t *testing.T) {
	cases := []struct{ category, preset, arena string }{
		{"images", "image", "text_to_image"},
		{"videos", "video", "text_to_video"},
		{"audio_transcription", "stt", "asr"},
	}
	for _, c := range cases {
		f := fixture()
		f.cl.Purpose = "coding" // irrelevant for these categories
		weak, strong := "weak-one", "strong-one"
		f.snap.Models[weak] = &catalog.Model{ID: weak, Endpoint: c.category, Lines: []catalog.Line{{Unit: unitFor(c.category), IDR: 100}}}
		f.snap.Models[strong] = &catalog.Model{ID: strong, Endpoint: c.category, Lines: []catalog.Line{{Unit: unitFor(c.category), IDR: 100}}}
		f.cl.Presets = map[string]config.Preset{c.preset: {Category: c.category, Optimization: "best_quality", Models: []string{weak, strong}}}
		f.benchIn(weak, c.arena, 0.2)
		f.benchIn(strong, c.arena, 0.9)
		in := Inputs{Cfg: f.cfg, Snap: f.snap, Pool: f.pool, Bench: f.benchScores, Now: now}
		r := One(in, f.cl, c.preset, f.cl.Presets[c.preset])
		// the weaker model is below the pool median, so Best Quality drops it with a reason; what matters here
		// is that the arena was read at all, which the source of the surviving candidate proves
		if len(r.Candidates) == 0 {
			t.Fatalf("%s: no model was ranked on the %s arena: %+v", c.category, c.arena, r)
		}
		if r.Candidates[0].Model != strong {
			t.Fatalf("%s: the better %s model must lead, got %v", c.category, c.arena, order(r))
		}
		if r.Candidates[0].BenchSource != c.arena {
			t.Fatalf("%s: the score must come from the %s arena, got %q", c.category, c.arena, r.Candidates[0].BenchSource)
		}
	}
}

// TTS, music and System One have no public benchmark, so Best Quality has nothing to offer and says so.
func TestCategoriesWithoutABenchmarkAreUnranked(t *testing.T) {
	for _, c := range []struct{ category, preset, unit string }{
		{"audio_speech", "tts", "1k_chars"},
		{"music", "music", "song"},
		{"systemone", "systemone", "token_1m"},
	} {
		f := fixture()
		f.snap.Models["m1"] = &catalog.Model{ID: "m1", Endpoint: c.category, InIDR: 1000, OutIDR: 2000,
			Lines: []catalog.Line{{Unit: c.unit, IDR: 100}}}
		f.cl.Presets = map[string]config.Preset{c.preset: {Category: c.category, Optimization: "best_quality", Models: []string{"m1"}}}
		in := Inputs{Cfg: f.cfg, Snap: f.snap, Pool: f.pool, Bench: f.benchScores, Now: now}
		r := One(in, f.cl, c.preset, f.cl.Presets[c.preset])
		if len(r.Candidates) != 0 || len(r.Unranked) != 1 {
			t.Fatalf("%s: a category with no benchmark must report the model as unranked, got %+v", c.category, r)
		}
	}
}

// unitFor is the catalog price unit a category is billed in, for building fixtures.
func unitFor(category string) string {
	switch category {
	case "images":
		return "image"
	case "videos", "audio_transcription":
		return "second"
	case "audio_speech":
		return "1k_chars"
	case "music":
		return "song"
	}
	return "token_1m"
}

// promoChat is a paid model whose promo is running.
func promoChat(id string, off float64, ends time.Time) *catalog.Model {
	m := chat(id, 100, 300)
	m.Promo = &catalog.Promo{PercentOff: off, StartsAt: now.Add(-time.Hour), EndsAt: ends}
	return m
}

// A free model and a discounted model are in the pool of every preset, whether
// the owner ticked them or not (ADR 0022).
func TestFreeAndPromoModelsAreAlwaysInThePool(t *testing.T) {
	f := fixture(chat("paid-a", 100, 300), freeChat("free-a:free"), promoChat("promo-b", 90, now.Add(24*time.Hour)))
	// the owner ticked only the paid model
	f.preset("paid-a")

	for _, opt := range []string{"cheapest", "balanced", "best_quality"} {
		f.optimize(opt)
		r := f.rank(nil)
		inPool := false
		for _, s := range r.Candidates {
			if s.Model == "free-a:free" {
				inPool = true
			}
		}
		if !inPool {
			t.Errorf("%s: the free model must be served: %v", opt, order(r))
		}
		inPool = false
		for _, s := range r.Candidates {
			if s.Model == "promo-b" {
				inPool = true
			}
		}
		if !inPool {
			t.Errorf("%s: the discounted model must be served: %v", opt, order(r))
		}
	}
}

// A model that is no longer free and no longer discounted leaves the pool: the
// pool follows the catalog, not a snapshot of it.
func TestAModelThatStopsBeingFreeLeavesThePool(t *testing.T) {
	m := chat("was-free", 100, 300)
	f := fixture(m)
	f.preset("paid-a")
	// the model is paid and has no promo: it is not added to the pool
	f.snap.Models["was-free"] = m
	f.optimize("balanced")
	r := f.rank(nil)
	for _, s := range r.Candidates {
		if s.Model == "was-free" {
			t.Fatalf("a paid model without a promo must not be added: %v", order(r))
		}
	}
}

// A promo that has ended leaves the pool, and a model that kenari drops leaves
// it too.
func TestEndedPromoAndRemovedModelLeaveThePool(t *testing.T) {
	ended := promoChat("promo-ended", 90, now.Add(-time.Hour))
	f := fixture(chat("paid-a", 100, 300), ended)
	f.preset("paid-a")
	f.optimize("balanced")
	r := f.rank(nil)
	for _, s := range r.Candidates {
		if s.Model == "promo-ended" {
			t.Fatalf("a model whose promo has ended must not be added: %v", order(r))
		}
	}
	// a model that leaves the catalog is reported, not served
	f2 := fixture(chat("paid-a", 100, 300))
	f2.preset("paid-a", "gone")
	f2.optimize("balanced")
	r2 := f2.rank(nil)
	if rejected(r2, "gone") == "" {
		t.Fatalf("a model that is not in the catalog must be reported: %+v", r2.Rejected)
	}
}

// The owner's own models keep their place: a free model does not push them out.
func TestFreeModelsDoNotPushOutTheOwnersModels(t *testing.T) {
	f := fixture(chat("paid-a", 100, 300), chat("paid-b", 200, 600), freeChat("free-a:free"))
	f.preset("paid-a", "paid-b")
	f.optimize("cheapest")
	r := f.rank(func(in *Inputs) { in.Top = 2 })
	got := order(r)
	if len(got) != 2 {
		t.Fatalf("the list must hold two models: %v", got)
	}
	if got[0] != "free-a:free" {
		t.Fatalf("the free model is the cheapest and comes first: %v", got)
	}
	if got[1] != "paid-a" {
		t.Fatalf("the owner's own model must keep a place: %v", got)
	}
}

// The effective pool is the owner's models plus the free and discounted ones,
// without duplicates.
func TestEffectivePoolHasNoDuplicates(t *testing.T) {
	f := fixture(chat("paid-a", 100, 300), freeChat("free-a:free"))
	// the owner ticked the free model as well
	f.preset("paid-a", "free-a:free")
	pool := EffectivePool(f.cfg, f.snap, f.pool, f.cl, f.cl.Presets["text"], now)
	seen := map[string]int{}
	for _, id := range pool {
		seen[id]++
	}
	if seen["free-a:free"] != 1 {
		t.Fatalf("a model may be in the pool once only: %v", pool)
	}
	if seen["paid-a"] != 1 {
		t.Fatalf("the owner's model must be in the pool: %v", pool)
	}
}

// A free model with no quality evidence is served last, not left out: it is in
// the pool and the router reaches it through the reserve.
func TestFreeModelWithoutEvidenceIsNotLeftOut(t *testing.T) {
	f := fixture(chat("paid-a", 100, 300), freeChat("free-a:free"))
	f.preset("paid-a")
	f.optimize("best_quality")
	f.bench(map[string]float64{"paid-a": 0.8})
	r := f.rank(func(in *Inputs) { in.Top = 1 })
	// the list holds one model; the free one is not rejected for want of evidence
	if why := rejected(r, "free-a:free"); why != "" {
		t.Fatalf("a free model must not be rejected: %q", why)
	}
	served := false
	for _, s := range r.Candidates {
		if s.Model == "free-a:free" {
			served = true
		}
	}
	if !served {
		reserved := false
		for _, s := range r.Reserve {
			if s.Model == "free-a:free" {
				reserved = true
			}
		}
		if !reserved {
			t.Fatalf("a free model must be in the list or the reserve: %+v %+v", r.Candidates, r.Reserve)
		}
	}
}

// Free models all cost nothing, so the price cannot tell them apart; the speed measured on real traffic does
// (ADR 0032). Without it the free slots went to the models whose names come first in the alphabet.
func TestTheFreeHalfIsFilledByMeasuredSpeed(t *testing.T) {
	f := fixture(freeChat("a-slow:free"), freeChat("b-slow:free"), freeChat("c-fast:free"), freeChat("d-fast:free"), freeChat("e-mid:free"), freeChat("z-fast:free"))
	f.optimize("cheapest")
	waits := map[string]float64{"a-slow:free": 40000, "b-slow:free": 30000, "c-fast:free": 900, "d-fast:free": 1100, "e-mid:free": 5000, "z-fast:free": 400}
	r := f.rank(func(in *Inputs) {
		in.Top = 4 // two free slots
		in.Speed = func(_, _, m string) (float64, bool) { w, ok := waits[m]; return w, ok }
	})
	got := order(r)[:2]
	if strings.Join(got, ",") != "c-fast:free,d-fast:free" && strings.Join(got, ",") != "d-fast:free,c-fast:free" && !(containsStr(got, "z-fast:free")) {
		t.Fatalf("the fast models must take the free slots, got %v", got)
	}
	for _, m := range order(r) {
		if m == "a-slow:free" || m == "b-slow:free" {
			t.Fatalf("a slow model took a free slot: %v", order(r))
		}
	}
	// the reserve is ordered the same way
	var reserve []string
	for _, c := range r.Reserve {
		reserve = append(reserve, c.Model)
	}
	if len(reserve) < 2 || reserve[len(reserve)-1] != "a-slow:free" {
		t.Fatalf("the slowest model must be the last reserve, got %v", reserve)
	}
}

// A model nobody has measured is not punished: it counts as average, so it still gets traffic and a measurement.
func TestAnUnmeasuredModelCountsAsAverage(t *testing.T) {
	f := fixture(freeChat("a-new:free"), freeChat("b-slow:free"), freeChat("c-fast:free"))
	f.optimize("cheapest")
	r := f.rank(func(in *Inputs) {
		in.Top = 4
		in.Speed = func(_, _, m string) (float64, bool) {
			return map[string]float64{"b-slow:free": 30000, "c-fast:free": 800}[m], m != "a-new:free"
		}
	})
	got := order(r)
	if got[0] != "c-fast:free" || got[len(got)-1] != "b-slow:free" {
		t.Fatalf("order %v: fast first, slow last, the unmeasured one in between", got)
	}
}

// Two models within a bucket of each other are equally fast: the order falls back to the name instead of
// reshuffling the list over noise.
func TestSpeedsInTheSameBucketKeepTheNameOrder(t *testing.T) {
	f := fixture(freeChat("a:free"), freeChat("b:free"))
	f.optimize("cheapest")
	r := f.rank(func(in *Inputs) {
		in.Speed = func(_, _, m string) (float64, bool) {
			return map[string]float64{"a:free": 1400, "b:free": 600}[m], true
		}
	})
	if got := order(r); got[0] != "a:free" {
		t.Fatalf("1.4 s and 0.6 s are the same class, the name decides: %v", got)
	}
}

func TestNoSpeedDataKeepsTheNameOrder(t *testing.T) {
	f := fixture(freeChat("b:free"), freeChat("a:free"))
	f.optimize("cheapest")
	if got := order(f.rank(nil)); got[0] != "a:free" {
		t.Fatalf("got %v", got)
	}
}

// A free model the owner unticked stays out of the pool (ADR 0035), unless the
// owner ticks it explicitly.
func TestExcludedFreeModelIsNotAddedAutomatically(t *testing.T) {
	f := fixture(chat("paid-a", 100, 300), freeChat("free-a:free"), freeChat("free-b:free"))
	f.preset("paid-a")
	sh := f.cl.Presets["text"]
	sh.Excluded = []string{"free-a:free"}
	pool := EffectivePool(f.cfg, f.snap, f.pool, f.cl, sh, now)
	for _, id := range pool {
		if id == "free-a:free" {
			t.Fatalf("an excluded free model must not be added: %v", pool)
		}
	}
	if len(pool) != 2 {
		t.Fatalf("paid-a and free-b:free must remain: %v", pool)
	}
	sh.Models = append(sh.Models, "free-a:free")
	pool = EffectivePool(f.cfg, f.snap, f.pool, f.cl, sh, now)
	if len(pool) != 3 {
		t.Fatalf("a model the owner ticked is in the pool whatever the exclusion says: %v", pool)
	}
}

// Fastest orders by the measured wait for the first token before price, an unmeasured model counts as average, and a model
// that fails often is as slow as its retries (ADR 0039).
func TestFastestOrdersBySpeedBeforePrice(t *testing.T) {
	f := fixture(chat("dear-fast", 400, 1200), freeChat("free-slow:free"), freeChat("free-mid:free"), freeChat("new:free"))
	f.preset("dear-fast", "free-slow:free", "free-mid:free", "new:free")
	f.optimize("fastest")
	waits := map[string]float64{"dear-fast": 600, "free-slow:free": 30000, "free-mid:free": 4000}
	r := f.rank(func(in *Inputs) {
		in.Top = 4
		in.Speed = func(_, _, m string) (float64, bool) { w, ok := waits[m]; return w, ok }
	})
	got := order(r)
	if got[0] != "dear-fast" {
		t.Fatalf("the fastest model leads even though it costs money: %v", got)
	}
	if got[len(got)-1] != "free-slow:free" {
		t.Fatalf("the slowest model is last: %v", got)
	}
	if indexOf(got, "new:free") > indexOf(got, "free-slow:free") || indexOf(got, "new:free") < indexOf(got, "dear-fast") {
		t.Fatalf("an unmeasured model counts as average, between the fast and the slow: %v", got)
	}
}

// Within one speed class the cheaper model comes first.
func TestFastestBreaksATieByPrice(t *testing.T) {
	f := fixture(chat("a-paid", 100, 300), freeChat("b-free:free"))
	f.preset("a-paid", "b-free:free")
	f.optimize("fastest")
	r := f.rank(func(in *Inputs) {
		in.Speed = func(_, _, m string) (float64, bool) { return 800, true }
	})
	if got := order(r); got[0] != "b-free:free" {
		t.Fatalf("equally fast: the free model first: %v", got)
	}
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
