package route

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/rank"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// snapSource is a catalog source that always answers with one snapshot, so a test never touches the network.
type snapSource struct{ snap *catalog.Snapshot }

func (s snapSource) Fetch(context.Context) (*catalog.Snapshot, error) { return s.snap, nil }

func chatModel(id string, in float64) *catalog.Model {
	return &catalog.Model{ID: id, Owner: "test", Endpoint: "chat", Endpoints: []string{"chat"}, Context: 128000,
		ToolCall: true, InIDR: in, OutIDR: in, CachedIDR: in / 10}
}

// setup builds a router over n models, all in one text preset, with one key.
func setup(ids ...string) (*Router, *config.Client) {
	cfg := config.Default()
	var models []*catalog.Model
	for i, id := range ids {
		models = append(models, chatModel(id, float64(1000*(i+1))))
	}
	cfg.Clients = []config.Client{{
		Name: "c", Enabled: true, Purpose: "general",
		Keys:    []config.Upstream{{Name: "k", KeyEnv: "K"}},
		Presets: map[string]config.Preset{"text": {Category: "chat", Optimization: "cheapest", Models: ids}},
	}}
	pool := upstream.NewPool()
	pool.Reload(cfg, map[string]string{"K": "s"}, nil)
	snap := &catalog.Snapshot{
		Models: map[string]*catalog.Model{},
		Tier:   catalog.FreeTier{Free: catalog.TierLimit{RPM: 1000, Daily: 0}, Topup: catalog.TierLimit{RPM: 1000}, Plan: catalog.TierLimit{RPM: 1000}},
	}
	for _, m := range models {
		snap.Models[m.ID] = m
	}
	cat := catalog.New(snapSource{snap: snap}, "")
	if err := cat.Refresh(context.Background()); err != nil {
		panic(err)
	}
	r := New(cfg, cat, pool, nil)
	r.Recompute(t0)
	return r, &cfg.Clients[0]
}

// Plan returns the models the plan would try, in order.
func planModels(p *Plan) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range p.Attempts {
		if !seen[a.Model.ID] {
			seen[a.Model.ID] = true
			out = append(out, a.Model.ID)
		}
	}
	return out
}

// Every model of the preset is used in turn: eleven fresh sessions over three models must use all three.
func TestRoundRobinUsesEveryModel(t *testing.T) {
	r, cl := setup("a", "b", "c")
	used := map[string]int{}
	for i := range 11 {
		p, err := r.Plan(cl, "text", "sess-"+string(rune('A'+i)), "/v1/chat/completions", t0.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		used[p.Model]++
	}
	if len(used) != 3 {
		t.Fatalf("round-robin must use every model, used %v", used)
	}
	for id, n := range used {
		if n < 3 {
			t.Fatalf("model %s was used %d times out of 11; the ring is not even: %v", id, n, used)
		}
	}
}

// A session keeps the model it started with, even though the ring moved on.
func TestSessionStaysOnOneModel(t *testing.T) {
	r, cl := setup("a", "b", "c")
	first, err := r.Plan(cl, "text", "session-1", "/v1/chat/completions", t0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	r.Pin(cl.Name, "text", "session-1", first.Model, t0)
	for i := range 6 {
		p, err := r.Plan(cl, "text", "session-1", "/v1/chat/completions", t0.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if p.Model != first.Model {
			t.Fatalf("session switched from %s to %s on request %d", first.Model, p.Model, i+2)
		}
	}
}

// Different sessions get different models: the ring is shared, the pin is per session.
func TestTwoSessionsDoNotShareAModel(t *testing.T) {
	r, cl := setup("a", "b")
	p1, _ := r.Plan(cl, "text", "one", "/v1/chat/completions", t0)
	r.Pin(cl.Name, "text", "one", p1.Model, t0)
	p2, _ := r.Plan(cl, "text", "two", "/v1/chat/completions", t0.Add(time.Second))
	if p1.Model == p2.Model {
		t.Fatalf("two fresh sessions got the same model %s; the ring did not advance", p1.Model)
	}
}

// A preset that does not stick routes every turn again: the same session moves
// through the pool instead of holding one model.
func TestANonStickyPresetRoutesEveryTurn(t *testing.T) {
	r, cl := setup("a", "b", "c")
	cl.Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"a", "b", "c"}, Sticky: new(false)}
	r.SetConfig(r.cfg.Load())

	seen := map[string]bool{}
	for i := range 6 {
		p, err := r.Plan(cl, "text", "same-session", "/v1/chat/completions", t0.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		seen[p.Model] = true
	}
	if len(seen) != 3 {
		t.Fatalf("a non-sticky preset must use every model of the pool, used %v", seen)
	}
	// and it keeps no pin at all
	if m, _ := r.PinOf(cl.Name, "text", "same-session"); m != "" {
		t.Fatalf("a non-sticky preset must not pin, pinned %s", m)
	}
}

// The toggle is per preset: a client may hold a sticky preset and a roaming
// one at the same time, and each behaves its own way.
func TestStickyIsPerPreset(t *testing.T) {
	r, cl := setup("a", "b")
	cl.Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"a", "b"}}
	cl.Presets["roam"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"a", "b"}, Sticky: new(false)}
	r.SetConfig(r.cfg.Load())

	first, err := r.Plan(cl, "text", "s1", "/v1/chat/completions", t0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	r.Pin(cl.Name, "text", "s1", first.Model, t0)
	for i := range 4 {
		p, err := r.Plan(cl, "text", "s1", "/v1/chat/completions", t0.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if p.Model != first.Model {
			t.Fatalf("the sticky preset moved from %s to %s", first.Model, p.Model)
		}
	}
	// the roaming preset of the same client moves the same session on
	moved := false
	for i := range 4 {
		p, err := r.Plan(cl, "roam", "s1", "/v1/chat/completions", t0.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if p.Model != first.Model {
			moved = true
		}
	}
	if !moved {
		t.Fatal("the roaming preset must not hold the session")
	}
}

// Three errors inside the window take a model out of that preset only, and the next model takes over.
func TestThreeErrorsEvictFromThatPresetOnly(t *testing.T) {
	r, cl := setup("a", "b")
	// a second preset on the same client, to prove eviction is per preset
	cl.Presets["text2"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"a", "b"}}
	r.SetConfig(r.cfg.Load())

	for i := range 3 {
		if evicted := r.Failed(cl.Name, "text", "a", t0.Add(time.Duration(i)*time.Minute)); evicted != (i == 2) {
			t.Fatalf("failure %d: evicted=%v, want %v", i+1, evicted, i == 2)
		}
	}
	if !r.evicted(cl.Name, "text", "a", t0.Add(4*time.Minute)) {
		t.Fatal("the model must be out of the preset after three errors")
	}
	if r.evicted(cl.Name, "text2", "a", t0.Add(4*time.Minute)) {
		t.Fatal("eviction must not follow the model into another preset")
	}
	// a plan for the affected preset must not offer the evicted model
	p, err := r.Plan(cl, "text", "fresh", "/v1/chat/completions", t0.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := planModels(p); len(got) != 1 || got[0] != "b" {
		t.Fatalf("the evicted model must be skipped and the next one used, got %v", got)
	}
	// the cooldown runs from the third failure, and the model comes back when it ends
	until, ok := r.Evictions(cl.Name, "text", t0.Add(5*time.Minute))["a"]
	if !ok {
		t.Fatal("the eviction must be listed with the moment it ends")
	}
	want := t0.Add(2*time.Minute + time.Duration(r.cfg.Load().Routing.EvictCooldownMin)*time.Minute)
	if !until.Equal(want) {
		t.Fatalf("cooldown ends at %v, want %v (12 h after the third failure)", until, want)
	}
	if r.evicted(cl.Name, "text", "a", until) {
		t.Fatal("the model must come back when the cooldown ends")
	}
}

// Three errors spread wider than the window do not evict: the record is "errors in a row", not "errors ever".
func TestErrorsOutsideWindowDoNotEvict(t *testing.T) {
	r, cl := setup("a", "b")
	window := time.Duration(r.cfg.Load().Routing.EvictWindowMin) * time.Minute
	r.Failed(cl.Name, "text", "a", t0)
	r.Failed(cl.Name, "text", "a", t0.Add(window/2))
	if r.Failed(cl.Name, "text", "a", t0.Add(window*2)) {
		t.Fatal("a failure after the window must not count towards eviction")
	}
	if r.evicted(cl.Name, "text", "a", t0.Add(window*2+time.Minute)) {
		t.Fatal("the model must still be usable")
	}
}

// A success clears the record, so two errors, a success and one more error do not evict.
func TestSuccessClearsFailureRecord(t *testing.T) {
	r, cl := setup("a", "b")
	r.Failed(cl.Name, "text", "a", t0)
	r.Failed(cl.Name, "text", "a", t0.Add(time.Minute))
	r.Succeeded(cl.Name, "text", "a")
	if r.Failed(cl.Name, "text", "a", t0.Add(2*time.Minute)) {
		t.Fatal("a success must reset the failure count")
	}
}

// Eviction beats stickiness: a session pinned to an evicted model moves on.
func TestEvictionOverridesSessionPin(t *testing.T) {
	r, cl := setup("a", "b")
	p, _ := r.Plan(cl, "text", "s1", "/v1/chat/completions", t0)
	r.Pin(cl.Name, "text", "s1", p.Model, t0)
	for range 3 {
		r.Failed(cl.Name, "text", p.Model, t0)
	}
	next, err := r.Plan(cl, "text", "s1", "/v1/chat/completions", t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if next.Model == p.Model {
		t.Fatalf("a session pinned to an evicted model must move: still on %s", p.Model)
	}
}

// When every model of a preset is out, the router says so instead of trying a cooling model.
func TestAllEvictedReportsAnError(t *testing.T) {
	r, cl := setup("a")
	for range 3 {
		r.Failed(cl.Name, "text", "a", t0)
	}
	if _, err := r.Plan(cl, "text", "s", "/v1/chat/completions", t0.Add(time.Minute)); err == nil {
		t.Fatal("a preset with every model evicted must fail with a reason")
	}
}

// A preset named on the wrong endpoint is refused: an image preset cannot serve a chat request.
func TestPresetCategoryMustMatchTheEndpoint(t *testing.T) {
	r, cl := setup("a")
	cl.Presets["image"] = config.Preset{Category: "images", Optimization: "cheapest", Models: []string{"a"}}
	r.SetConfig(r.cfg.Load())
	if _, err := r.Plan(cl, "image", "s", "/v1/chat/completions", t0); err == nil {
		t.Fatal("an images preset must not answer a chat request")
	}
}

// An endpoint xellter does not serve is refused, not forwarded. A chat preset used to answer any path, so a
// request for an endpoint xellter has was sent upstream as a model call: its failure was counted against
// every model of the preset, and three such failures took the whole pool out for hours.
func TestUnknownEndpointIsRefused(t *testing.T) {
	r, cl := setup("a", "b", "c")
	_, err := r.Plan(cl, "text", "s", "/v1/x/search", t0)
	if err == nil {
		t.Fatal("an endpoint xellter does not serve must be refused")
	}
	if err.Status != 404 {
		t.Fatalf("status = %d, want 404", err.Status)
	}
	// The same holds for a model named directly.
	if _, err := r.Plan(cl, "a", "s", "/v1/x/search", t0); err == nil {
		t.Fatal("a model must not be callable on an endpoint xellter does not serve")
	}
	// The chat path is untouched.
	if _, err := r.Plan(cl, "text", "s", "/v1/chat/completions", t0); err != nil {
		t.Fatalf("the chat path must still work: %v", err)
	}
}

// A client may name a model that is in one of its own pools, and nothing else.
func TestDirectModelMustBeInAPool(t *testing.T) {
	r, cl := setup("a")
	r.cat.Snapshot().Models["stranger"] = chatModel("stranger", 500)
	r.Recompute(t0)
	if _, err := r.Plan(cl, "a", "s", "/v1/chat/completions", t0); err != nil {
		t.Fatalf("a model in the pool must be callable by name: %v", err)
	}
	if _, err := r.Plan(cl, "stranger", "s", "/v1/chat/completions", t0); err == nil {
		t.Fatal("a model outside every pool must be refused")
	}
}

// The ranking is recomputed on demand (the Recalculate button) and the timestamp moves.
func TestRecomputeMarksItsTime(t *testing.T) {
	r, _ := setup("a", "b")
	if !r.Computed().Equal(t0) {
		t.Fatalf("computed = %v, want %v", r.Computed(), t0)
	}
	later := t0.Add(5 * time.Minute)
	r.Recompute(later)
	if !r.Computed().Equal(later) {
		t.Fatalf("after recompute, computed = %v, want %v", r.Computed(), later)
	}
}

// Rankings are keyed per client and preset.
func TestRankingsAreKeyedPerClientPreset(t *testing.T) {
	r, _ := setup("a")
	rk := r.Rankings()[rank.Key("c", "text")]
	if rk == nil {
		t.Fatal("the preset must have a ranking")
	}
	if rk.Client != "c" || rk.Preset != "text" {
		t.Fatalf("ranking is for %s/%s", rk.Client, rk.Preset)
	}
}

// System One models are reached through the chat endpoint, so a systemone preset must serve a chat request
// while a text preset still refuses an image request (ADR 0019).
func TestSystemOneServesTheChatPath(t *testing.T) {
	r, cl := setup("a")
	r.cat.Snapshot().Models["sys"] = &catalog.Model{ID: "sys", Endpoint: "systemone", Endpoints: []string{"systemone"}, InIDR: 1000, OutIDR: 2000}
	cl.Presets["systemone"] = config.Preset{Category: "systemone", Optimization: "cheapest", Models: []string{"sys"}}
	r.SetConfig(r.cfg.Load())

	if _, err := r.Plan(cl, "systemone", "s", "/v1/chat/completions", t0); err != nil {
		t.Fatalf("a systemone preset must serve a chat request: %v", err)
	}
	if _, err := r.Plan(cl, "text", "s", "/v1/images/generations", t0); err == nil {
		t.Fatal("a text preset must not serve an image request")
	}
}

// A model outside the top list is promoted when a model above it is evicted: the preset keeps serving the same
// number of models (ADR 0020).
func TestEvictionPromotesAReserveModel(t *testing.T) {
	r, _ := setup("a", "b", "c", "d")
	cfg := *r.cfg.Load()      // a copy: the published config is never mutated in place
	cfg.Routing.TopModels = 2 // a and b serve; c and d are reserves
	r.SetConfig(&cfg)
	cl := &cfg.Clients[0]
	rk := r.Rankings()["c/text"]
	if len(rk.Candidates) != 2 || len(rk.Reserve) != 2 {
		t.Fatalf("expected 2 serving and 2 reserve models, got %d/%d", len(rk.Candidates), len(rk.Reserve))
	}
	top := rk.Candidates[0].Model
	for range 3 {
		r.Failed(cl.Name, "text", top, t0)
	}
	p, err := r.Plan(cl, "text", "s", "/v1/chat/completions", t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := planModels(p)
	if len(got) < 2 {
		t.Fatalf("a reserve must take the evicted model's place, got %v", got)
	}
	if got[0] == top {
		t.Fatalf("the evicted model must not serve: %v", got)
	}
}

func TestPinsSurviveARestart(t *testing.T) {
	r, cl := setup("a", "b", "c")
	r.Pin(cl.Name, "text", "s1", "b", t0)
	saved, v := r.PinState()
	if len(saved) != 1 || v == 0 {
		t.Fatalf("one pin and a version expected, got %d pins, version %d", len(saved), v)
	}
	r2, _ := setup("a", "b", "c")
	if n, _ := r2.RestorePins(saved, t0, time.Hour); n != 1 {
		t.Fatalf("restored %d pins, want 1", n)
	}
	if got, _ := r2.PinOf(cl.Name, "text", "s1"); got != "b" {
		t.Fatalf("pin after restart = %q, want b", got)
	}
	r3, _ := setup("a", "b", "c")
	if n, _ := r3.RestorePins(saved, t0.Add(2*time.Hour), time.Hour); n != 0 {
		t.Fatalf("a pin older than the limit must not be restored, got %d", n)
	}
}

// feed records n requests of one model in one preset and returns what the last one decided.
func feed(r *Router, preset, model string, n int, ttft time.Duration, ok bool, at time.Time) (string, time.Time) {
	var why string
	for i := 0; i < n; i++ {
		at = at.Add(10 * time.Second)
		why, _ = r.Observe("c", preset, "", model, ttft, ok, at)
	}
	return why, at
}

func TestASlowModelIsTakenOutWithItsReasonAndComesBack(t *testing.T) {
	r, _ := setup("a", "b", "c", "d")
	if why, _ := feed(r, "text", "a", 5, 20*time.Second, true, t0); why != "" {
		t.Fatalf("five requests are too few to judge a model: %s", why)
	}
	why, at := feed(r, "text", "a", 1, 20*time.Second, true, t0.Add(time.Minute))
	if !strings.HasPrefix(why, "slow: median first token 20.0s") {
		t.Fatalf("reason = %q", why)
	}
	out := r.OutOfService("c", "text", at)
	if len(out) != 1 || out[0].Model != "a" || out[0].Kind != "slow" || out[0].Reason != why {
		t.Fatalf("out of service = %+v", out)
	}
	if got := out[0].Until.Sub(at); got != 30*time.Minute {
		t.Fatalf("first cooldown %s, want 30m", got)
	}
	// the other models of the preset keep serving
	p, err := r.Plan(&config.Client{Name: "c", Presets: map[string]config.Preset{"text": {Category: "chat"}}}, "text", "", "/v1/chat/completions", at)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range planModels(p) {
		if m == "a" {
			t.Fatal("a model that is out was planned")
		}
	}
	// it comes back after the cooldown, and a single slow request is not enough to judge it again... but
	// half the usual number is: it was already seen being slow
	back := out[0].Until.Add(time.Second)
	if len(r.OutOfService("c", "text", back)) != 0 {
		t.Fatal("the model must be back after its cooldown")
	}
	if why, _ := feed(r, "text", "a", 2, 20*time.Second, true, back); why != "" {
		t.Fatalf("two requests are too few even on probation (need 3): %s", why)
	}
	why, at = feed(r, "text", "a", 1, 20*time.Second, true, back.Add(time.Minute))
	if why == "" {
		t.Fatal("a model that is slow again on probation must go out again")
	}
	if got := r.OutOfService("c", "text", at)[0].Until.Sub(at); got != time.Hour {
		t.Fatalf("second cooldown %s, want 1h (doubled)", got)
	}
}

func TestAHealthyModelOnProbationResetsItsCooldown(t *testing.T) {
	r, _ := setup("a", "b", "c")
	_, at := feed(r, "text", "a", 6, 20*time.Second, true, t0)
	back := r.OutOfService("c", "text", at)[0].Until.Add(time.Second)
	if why, at2 := feed(r, "text", "a", 3, time.Second, true, back); why != "" {
		t.Fatalf("a fast model was taken out: %s", why)
	} else {
		at = at2
	}
	// slow later, from scratch: the cooldown is the short one again
	_, at = feed(r, "text", "a", 40, 20*time.Second, true, at.Add(2*time.Hour))
	if o := r.OutOfService("c", "text", at)[0]; o.Until.Sub(o.Since) != 30*time.Minute {
		t.Fatalf("cooldown %s, want the short one after a healthy spell", o.Until.Sub(o.Since))
	}
}

func TestAnUnreliableModelIsTakenOut(t *testing.T) {
	r, _ := setup("a", "b", "c")
	for i := 0; i < 3; i++ {
		r.Observe("c", "text", "", "a", time.Second, true, t0.Add(time.Duration(i)*time.Second))
	}
	why, _ := feed(r, "text", "a", 3, 0, false, t0.Add(time.Minute))
	if !strings.HasPrefix(why, "unreliable: 3 of its last 6 requests failed") {
		t.Fatalf("reason = %q", why)
	}
}

func TestAModelIsOutOnlyOfThePresetThatSawItSlow(t *testing.T) {
	r, _ := setup("a", "b", "c")
	_, at := feed(r, "text", "a", 6, 20*time.Second, true, t0)
	if len(r.OutOfService("c", "other", at)) != 0 || len(r.OutOfService("someone-else", "text", at)) != 0 {
		t.Fatal("a model went out of a preset or a client that did not see it slow")
	}
}

func TestASessionLeavesAModelThatKeepsBeingSlow(t *testing.T) {
	r, cl := setup("a", "b", "c")
	r.Pin("c", "text", "s1", "a", t0)
	fast, slow := 2*time.Second, 20*time.Second
	if _, moved := r.Observe("c", "text", "s1", "a", slow, true, t0); moved {
		t.Fatal("one slow answer must not move a session")
	}
	r.Observe("c", "text", "s1", "a", fast, true, t0) // a side call that was quick
	if _, moved := r.Observe("c", "text", "s1", "a", slow, true, t0); !moved {
		t.Fatal("two slow answers in the last three must move the session")
	}
	for i := 0; i < 6; i++ {
		p, err := r.Plan(cl, "text", "s1", "/v1/chat/completions", t0.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if p.Model == "a" {
			t.Fatal("the session went back to the model it left")
		}
	}
}

func TestThePresetIsNeverEmptied(t *testing.T) {
	r, _ := setup("a", "b", "c")
	_, at := feed(r, "text", "a", 6, 20*time.Second, true, t0)
	// a is out; b going out would leave only c, and the last two models stay in service
	if why, _ := feed(r, "text", "b", 12, 20*time.Second, true, at.Add(time.Minute)); why != "" {
		t.Fatalf("the last models of a preset must stay in service: %s", why)
	}
}
