package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/store"
)

// A reasoning effort is the client's request to the model. The gateway forwards it, never chooses a model by it
// (ADR 0028), and serves a model that refuses the field without it (ADR 0026).

// The effort is the client's request to the model, not a routing key (ADR 0028). A side
// call (a title, a summary) asks for "none" while the conversation asks for something
// else; if the effort moved a request between models, the side call would take the
// session's pin with it.
func TestTheEffortDoesNotMoveASessionToAnotherModel(t *testing.T) {
	e := setup(t, nil)
	sess := []string{"X-Xellter-Session", "s-effort"}
	post := func(effort string) string {
		resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"`+effort+`"}`, sess...)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		return resp.Header.Get("X-Xellter-Model")
	}
	first := post("minimal")
	for i := 0; i < 6; i++ {
		effort := []string{"none", "minimal", "low"}[i%3]
		if got := post(effort); got != first {
			t.Fatalf("request %d (effort %s) moved the session from %s to %s", i, effort, first, got)
		}
	}
}

func TestAModelThatRejectsTheEffortIsNotAskedAgain(t *testing.T) {
	e := setup(t, nil)
	e.a.Rt.Efforts().Record("cheap-b", "none", time.Now())
	e.a.Rt.Efforts().Record("pro-c", "none", time.Now())
	e.a.Rt.Efforts().Record("free-a:free", "none", time.Now())
	e.a.Rt.Efforts().Record("promo-d", "none", time.Now())

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
	if ok := e.a.Rt.Efforts().Refuses(resp.Header.Get("X-Xellter-Model"), "none"); !ok {
		t.Fatal("the refusal was not recorded")
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

// A refusal is a property of the API's parameter validation, not a passing mood. It holds however old it is,
// and after a restart the model is still not asked with the field.
func TestARefusalSurvivesARestart(t *testing.T) {
	e := setup(t, nil)
	e.a.Rt.Efforts().Record("cheap-b", "none", time.Now().Add(-1000*24*time.Hour))
	e.a.Close()

	a2 := reopen(t, e)
	defer a2.Close()
	if !a2.Rt.Efforts().Refuses("cheap-b", "none") {
		t.Fatal("the refusal did not survive the restart")
	}
}

// The table once held what a model did with an effort ("honours", "ignores"). Those rows are not used any more
// (ADR 0028): they must neither route anything nor be mistaken for a refusal.
func TestOldMeasurementsAreIgnoredAfterARestart(t *testing.T) {
	e := setup(t, nil)
	if err := e.a.St.SaveEffortFacts([]store.EffortFact{
		{Model: "cheap-b", Level: "none", Verdict: "ignores", At: 1},
		{Model: "pro-c", Level: "none", Verdict: "honours", At: 1},
		{Model: "free-a:free", Level: "none", Verdict: "rejects", At: 1},
	}); err != nil {
		t.Fatal(err)
	}
	e.a.Close()

	a2 := reopen(t, e)
	defer a2.Close()
	if a2.Rt.Efforts().Refuses("cheap-b", "none") || a2.Rt.Efforts().Refuses("pro-c", "none") {
		t.Fatal("a measurement that is not a refusal was loaded as one")
	}
	if !a2.Rt.Efforts().Refuses("free-a:free", "none") {
		t.Fatal("a refusal was not loaded")
	}
}

// A read-only command such as `xellter explain` builds an app over the same data directory, loads the refusals
// and closes again without serving anything. It must not write its older copy back over what the running
// gateway has learned since.
func TestAReadOnlyCommandDoesNotOverwriteWhatWasLearned(t *testing.T) {
	e := setup(t, nil)
	e.a.Rt.Efforts().Record("cheap-b", "none", time.Now())
	e.a.Close()

	src := &catalog.Kenari{BaseURL: e.f.srv.URL, UA: "test", HTTP: e.f.srv.Client()}
	reader, err := app.New(e.path, src) // the read-only command: loads, holds
	if err != nil {
		t.Fatal(err)
	}
	running, err := app.New(e.path, src) // the running gateway learns something new and writes it
	if err != nil {
		t.Fatal(err)
	}
	running.Rt.Efforts().Record("pro-c", "none", time.Now())
	running.Close()
	reader.Close() // it learned nothing, so it writes nothing

	verify, err := app.New(e.path, src)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	if n := len(verify.St.EffortFacts()); n != 2 {
		t.Fatalf("the read-only command overwrote the refusals: %v", verify.St.EffortFacts())
	}
}

// reopen builds a new app over the data directory of e, as a restart does.
func reopen(t *testing.T, e *env) *app.App {
	t.Helper()
	src := &catalog.Kenari{BaseURL: e.f.srv.URL, UA: "test", HTTP: e.f.srv.Client()}
	a, err := app.New(e.path, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Rt.Recompute(time.Now())
	return a
}
