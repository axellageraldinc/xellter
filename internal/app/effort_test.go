package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/effort"
)

// The verdicts the tests write into the registry. They are spelled out here so the
// test reads as the behaviour it checks, not as a package qualifier.
const (
	effortHonours = effort.Honours
	effortIgnores = effort.Ignores
	effortRejects = effort.Rejects
	effortUnknown = effort.Unknown
)

func newEffortRegistry() *effort.Registry { return effort.NewRegistry() }

// A reasoning effort is a promise the gateway keeps (ADR 0026). These tests cover the
// three ways a model can break that promise, and the two things the gateway does
// about each.

func TestEffortNoneIsRoutedToAModelThatHonoursIt(t *testing.T) {
	e := setup(t, nil)
	// The pool has four chat models. Two of them are known to ignore "none" (they
	// were seen thinking), and two are known to honour it.
	e.a.Rt.Efforts().Record("cheap-b", "none", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("pro-c", "none", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("free-a:free", "none", effortHonours, time.Now())
	e.a.Rt.Efforts().Record("promo-d", "none", effortHonours, time.Now())

	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	// The model that must serve the request is the one that honours the effort.
	served := resp.Header.Get("X-Xellter-Model")
	if served != "free-a:free" && served != "promo-d" {
		t.Fatalf("a model that ignores the effort served the request: %s", served)
	}
}

func TestEffortNoneStillAnswersWhenEveryModelIgnoresIt(t *testing.T) {
	e := setup(t, nil)
	e.a.Rt.Efforts().Record("cheap-b", "none", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("pro-c", "none", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("free-a:free", "none", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("promo-d", "none", effortIgnores, time.Now())

	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("a client must still be answered when no model can honour the effort: status %d %s", resp.StatusCode, body)
	}
}

func TestAModelThatRejectsTheEffortIsServedWithoutIt(t *testing.T) {
	e := setup(t, nil)
	// The fake upstream refuses any body that carries the effort, exactly as
	// flint-v1-0:free does against kenari.
	e.f.chat = func(c call, n int) (int, string) {
		if strings.Contains(c.Body, "reasoning_effort") {
			return 400, `{"error":{"code":"upstream_rejected","message":"the model's provider rejected the request. check the model id and request fields","param":null,"type":"invalid_request_error"}}`
		}
		return 0, ""
	}

	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("the call must succeed even though the upstream refused the parameter: status %d %s", resp.StatusCode, body)
	}
	// The refusal was seen: the model is now known to reject the effort, so the next
	// request does not carry it at all.
	if got := e.a.Rt.Efforts().Verdict(resp.Header.Get("X-Xellter-Model"), "none"); got != effortRejects {
		t.Fatalf("the refusal was not recorded: %v", got)
	}
}

func TestAModelThatRejectsTheEffortIsNotAskedAgain(t *testing.T) {
	e := setup(t, nil)
	e.a.Rt.Efforts().Record("cheap-b", "none", effortRejects, time.Now())
	e.a.Rt.Efforts().Record("pro-c", "none", effortRejects, time.Now())
	e.a.Rt.Efforts().Record("free-a:free", "none", effortRejects, time.Now())
	e.a.Rt.Efforts().Record("promo-d", "none", effortRejects, time.Now())

	var sawEffort bool
	e.f.chat = func(c call, n int) (int, string) {
		if strings.Contains(c.Body, "reasoning_effort") {
			sawEffort = true
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if sawEffort {
		t.Fatal("the effort was sent to a model that is known to reject it")
	}
}

func TestAGradedEffortIsNotARoutingConstraint(t *testing.T) {
	e := setup(t, nil)
	// A model that ignores "low" is still a fine model for a "low" request: it
	// answers, it just answers with more thinking. Refusing to serve it would be
	// worse than the inaccuracy.
	e.a.Rt.Efforts().Record("cheap-b", "low", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("pro-c", "low", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("free-a:free", "low", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("promo-d", "low", effortIgnores, time.Now())

	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if served := resp.Header.Get("X-Xellter-Model"); served == "" {
		t.Fatal("no model served the request")
	}
}

// A model that thinks is known to think, so when it stays quiet under "none" it is
// credited with obeying the effort. A model that has never been seen thinking is not
// credited with anything: there is no evidence either way.
func TestASilentModelIsNotCreditedWithObeyingTheEffort(t *testing.T) {
	reg := newEffortRegistry()
	now := time.Now()
	// A model that has never thought, asked to not think, says nothing.
	if v := reg.Observe("quiet", "none", false, now); v != effortUnknown {
		t.Fatalf("a model that never thought must stay unknown, got %v", v)
	}
	// The same model, seen thinking: now a quiet answer is obedience.
	reg.Observe("thinker", "none", true, now)
	if v := reg.Observe("thinker", "none", false, now); v != effortHonours {
		t.Fatalf("a model that stopped thinking honoured the effort, got %v", v)
	}
	// A model that keeps thinking ignores the effort.
	if v := reg.Observe("thinker", "none", true, now); v != effortIgnores {
		t.Fatalf("a model that kept thinking ignored the effort, got %v", v)
	}
}

// A preset advertises the largest window in its pool: a client that keeps a long
// conversation must not throw it away for a window the served model never had.
func TestAPresetAdvertisesTheLargestContextInItsPool(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.get(t, e.keyCl, "/v1/models")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var list struct {
		Data []struct {
			ID            string `json:"id"`
			Preset        bool   `json:"preset"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	// The pool is free-a:free (128000), cheap-b (128000), pro-c (200000), promo-d
	// (128000). The largest is 200000.
	for _, m := range list.Data {
		if m.ID != "text" {
			continue
		}
		if m.ContextLength != 200000 {
			t.Fatalf("the preset advertised %d, the largest window in its pool is 200000", m.ContextLength)
		}
		return
	}
	t.Fatal("the preset was not in the model list")
}

// A conversation that does not fit the model that served it is the caller's history,
// not a fault of the model: it must not count against the model, or a healthy model
// is evicted for something the model did not do.
func TestAContextOverflowDoesNotCountAgainstTheModel(t *testing.T) {
	e := setup(t, nil)
	e.f.chat = func(c call, n int) (int, string) {
		return 400, `{"error":{"code":"context_length_exceeded","message":"the request exceeds the model's maximum context length. reduce the input or max_tokens","param":null,"type":"invalid_request_error"}}`
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode == 200 {
		t.Fatal("the upstream refused, so the client must see the refusal")
	}
	// The refusal is the upstream's own, not a gateway error.
	if !strings.Contains(body, "context_length_exceeded") {
		t.Fatalf("the upstream's own error was not passed on: %s", body)
	}
	// The model is not blamed: the same model may be tried again straight away.
	if len(e.a.Rt.Evictions("babu", "text", time.Now())) != 0 {
		t.Fatal("a context overflow evicted the model")
	}
}

// The effort a client asks for reaches the upstream as it asked.
func TestTheEffortReachesTheUpstream(t *testing.T) {
	e := setup(t, nil)
	var seen string
	e.f.chat = func(c call, n int) (int, string) {
		var body struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		_ = json.Unmarshal([]byte(c.Body), &body)
		seen = body.ReasoningEffort
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if seen != "none" {
		t.Fatalf("the upstream saw effort %q, wanted \"none\"", seen)
	}
}

// A request that asks for nothing is not a request for "none": the upstream applies
// its own default, and the gateway must not turn that into an explicit "none".
func TestARequestWithoutAnEffortCarriesNoEffort(t *testing.T) {
	e := setup(t, nil)
	var seen string
	var had bool
	e.f.chat = func(c call, n int) (int, string) {
		var body map[string]any
		_ = json.Unmarshal([]byte(c.Body), &body)
		v, ok := body["reasoning_effort"]
		had = ok
		if ok {
			seen, _ = v.(string)
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if had {
		t.Fatalf("the gateway invented an effort: %q", seen)
	}
}

// The nested shape a client may use is understood too.
func TestTheNestedEffortShapeIsUnderstood(t *testing.T) {
	e := setup(t, nil)
	var seen string
	e.f.chat = func(c call, n int) (int, string) {
		var body struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		_ = json.Unmarshal([]byte(c.Body), &body)
		seen = body.Reasoning.Effort
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"none"}}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if seen != "none" {
		t.Fatalf("the upstream saw effort %q, wanted \"none\"", seen)
	}
}

// The model list tells a client which efforts a model supports, from the metadata
// kenari publishes.
func TestTheModelListCarriesTheSupportedEfforts(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.get(t, e.keyCl, "/v1/models")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	// pro-c publishes "none, low, medium, high" in kenari's own catalog, and the
	// gateway passes it on so a client can offer the right choices.
	var list struct {
		Data []struct {
			ID           string   `json:"id"`
			EffortLevels []string `json:"effort_levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	for _, m := range list.Data {
		if m.ID != "pro-c" {
			continue
		}
		if len(m.EffortLevels) == 0 || m.EffortLevels[0] != "none" {
			t.Fatalf("the effort levels of pro-c were not published: %v", m.EffortLevels)
		}
		return
	}
	t.Fatal("pro-c was not in the model list")
}

// ---- persistence across a restart ----

// A restart must not cost the client the knowledge the gateway had already paid for.
//
// The registry is built from traffic, so without saving it every restart starts blind:
// the first request is served by a model that ignores the effort, and the client waits
// for a long answer it did not ask for. The gateway writes what it learned to the
// database and reads it back on the next start.
func TestWhatTheGatewayLearnedSurvivesARestart(t *testing.T) {
	e := setup(t, nil)
	// The gateway learns that two models ignore "none" and two honour it.
	e.a.Rt.Efforts().Record("cheap-b", "none", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("pro-c", "none", effortIgnores, time.Now())
	e.a.Rt.Efforts().Record("free-a:free", "none", effortHonours, time.Now())
	e.a.Rt.Efforts().Record("promo-d", "none", effortHonours, time.Now())
	// The facts are written when the service stops.
	e.a.Close()

	// A new process over the same data directory: the same as a restart.
	src := &catalog.Kenari{BaseURL: e.f.srv.URL, UA: "test", HTTP: e.f.srv.Client()}
	a2, err := app.New(e.path, src)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	if err := a2.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a2.Rt.Recompute(time.Now())

	// The knowledge came back.
	if v := a2.Rt.Efforts().Verdict("cheap-b", "none"); v != effortIgnores {
		t.Fatalf("the verdict for cheap-b came back as %v, wanted ignores", v)
	}
	if v := a2.Rt.Efforts().Verdict("free-a:free", "none"); v != effortHonours {
		t.Fatalf("the verdict for free-a:free came back as %v, wanted honours", v)
	}

	// And the routing uses it: the request is served by a model that honours the effort.
	px := httptest.NewServer(a2.Px)
	defer px.Close()
	req, _ := http.NewRequest("POST", px.URL+"/v1/chat/completions", strings.NewReader(`{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.keyCl)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	served := resp.Header.Get("X-Xellter-Model")
	if served != "free-a:free" && served != "promo-d" {
		t.Fatalf("after the restart a model that ignores the effort served the request: %s", served)
	}
}

// A fact is a measurement, and a provider can change a model's behaviour. A fact older
// than the trust window is not used, so the model is measured again.
func TestAnOldFactIsNotUsedAfterARestart(t *testing.T) {
	e := setup(t, nil)
	// A fact from long ago: the model honoured "none" back then.
	old := time.Now().Add(-2 * effort.DefaultTTL)
	e.a.Rt.Efforts().Record("cheap-b", "none", effortIgnores, old)
	e.a.Rt.Efforts().Record("pro-c", "none", effortIgnores, old)
	e.a.Rt.Efforts().Record("free-a:free", "none", effortHonours, old)
	e.a.Rt.Efforts().Record("promo-d", "none", effortHonours, old)
	e.a.Close()

	src := &catalog.Kenari{BaseURL: e.f.srv.URL, UA: "test", HTTP: e.f.srv.Client()}
	a2, err := app.New(e.path, src)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	if err := a2.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a2.Rt.Recompute(time.Now())

	// The old facts were dropped, so the models are unknown again and the routing does
	// not prefer the one that honoured the effort back then.
	if v := a2.Rt.Efforts().Verdict("free-a:free", "none"); v != effortUnknown {
		t.Fatalf("an expired fact was kept: %v", v)
	}
}

// A refusal is a property of the API's parameter validation, not a passing mood. It
// holds however old it is, and after a restart the model is still not asked with the
// field.
func TestARefusalSurvivesARestart(t *testing.T) {
	e := setup(t, nil)
	old := time.Now().Add(-100 * effort.DefaultTTL)
	e.a.Rt.Efforts().Record("cheap-b", "none", effortRejects, old)
	e.a.Close()

	src := &catalog.Kenari{BaseURL: e.f.srv.URL, UA: "test", HTTP: e.f.srv.Client()}
	a2, err := app.New(e.path, src)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	if err := a2.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a2.Rt.Recompute(time.Now())

	if v := a2.Rt.Efforts().Verdict("cheap-b", "none"); v != effortRejects {
		t.Fatalf("the refusal did not survive the restart: %v", v)
	}
}

// A read-only command such as `xellter explain` builds an app over the same data
// directory, loads the facts and closes again without serving anything. It must not
// write that older copy back over what the running gateway has learned since.
func TestAReadOnlyCommandDoesNotOverwriteWhatWasLearned(t *testing.T) {
	e := setup(t, nil)
	e.a.Rt.Efforts().Record("cheap-b", "none", effortIgnores, time.Now())
	e.a.Close() // a planned stop writes what was learned

	src := &catalog.Kenari{BaseURL: e.f.srv.URL, UA: "test", HTTP: e.f.srv.Client()}

	// A read-only command starts: it loads the facts and holds them.
	reader, err := app.New(e.path, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Meanwhile the running gateway learns something new and writes it.
	running, err := app.New(e.path, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := running.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	running.Rt.Efforts().Record("pro-c", "none", effortHonours, time.Now())
	running.Close()

	// The read-only command now finishes and closes. It learned nothing, so it must
	// not write its older copy back over what the running gateway just wrote.
	reader.Close()

	// The facts are still the ones the running gateway wrote.
	verify, err := app.New(e.path, src)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	facts, _ := verify.St.EffortFacts()
	if len(facts) != 2 {
		t.Fatalf("the read-only command overwrote the facts: %v", facts)
	}
	byModel := map[string]string{}
	for _, f := range facts {
		byModel[f.Model] = f.Verdict
	}
	if byModel["pro-c"] != "honours" || byModel["cheap-b"] != "ignores" {
		t.Fatalf("the facts came back as %v", byModel)
	}
}
