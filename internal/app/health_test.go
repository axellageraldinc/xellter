package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
)

// A model that sends its headers and then nothing is a model queueing. The caller must not wait for it
// when another model can answer (ADR 0029).
func TestAStreamThatStaysSilentMovesToTheNextModel(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.Routing.FirstTokenSecs = 1 })
	e.f.stall = map[string]time.Duration{"free-a:free": 8 * time.Second, "cheap-b": 8 * time.Second, "pro-c": 8 * time.Second}
	start := time.Now()
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "t0") {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Xellter-Model"); got != "promo-d" {
		t.Fatalf("served by %s, want the one model that answers", got)
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Fatalf("waited %s for silent models", took)
	}
}

// The last model that can be tried is never timed out: a slow answer beats none.
func TestTheLastModelIsWaitedFor(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.Routing.FirstTokenSecs = 1 })
	e.f.stall = map[string]time.Duration{"free-a:free": 2500 * time.Millisecond, "cheap-b": 2500 * time.Millisecond, "pro-c": 2500 * time.Millisecond, "promo-d": 2500 * time.Millisecond}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "t0") {
		t.Fatalf("a slow answer must still arrive: %d %s", resp.StatusCode, body)
	}
}

// A client that names no session is told apart by the first user message, not by the system prompt every
// one of its conversations shares (ADR 0030).
func TestConversationsWithTheSamePromptAreNotOneSession(t *testing.T) {
	e := setup(t, nil)
	sys := strings.Repeat("You are a helpful assistant. ", 40) // longer than the bytes the old key looked at
	post := func(user string) string {
		resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"system","content":"`+sys+`"},{"role":"user","content":"`+user+`"}]}`)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		return resp.Header.Get("X-Xellter-Model")
	}
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		seen[post("question number "+string(rune('a'+i)))] = true
	}
	if len(seen) < 2 {
		t.Fatalf("eight different conversations all went to %v: they share one pin", seen)
	}
	// and a conversation keeps its model as it grows
	first := post("a long talk")
	for i := 0; i < 4; i++ {
		resp, _ := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","messages":[{"role":"system","content":"`+sys+`"},{"role":"user","content":"a long talk"},{"role":"assistant","content":"ok"},{"role":"user","content":"and then?"}]}`)
		if got := resp.Header.Get("X-Xellter-Model"); got != first {
			t.Fatalf("turn %d moved from %s to %s", i+2, first, got)
		}
	}
}

// A model that does not answer within first_byte_timeout_s is slow, not gone: it is logged as a timeout
// (not as the caller leaving) and the next model answers.
func TestAFirstByteTimeoutIsLoggedAsATimeout(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.Kenari.FirstByteSecs = 1 })
	hung := map[string]bool{"free-a:free": true, "cheap-b": true, "pro-c": true}
	e.f.chat = func(c call, n int) (int, string) {
		if hung[c.Model] {
			time.Sleep(2 * time.Second)
		}
		return 0, ""
	}
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || resp.Header.Get("X-Xellter-Model") != "promo-d" {
		t.Fatalf("status %d model %s: %s", resp.StatusCode, resp.Header.Get("X-Xellter-Model"), body)
	}
	e.a.St.Flush()
	rows, err := e.a.St.Recent(20, "babu")
	if err != nil {
		t.Fatal(err)
	}
	timeouts := 0
	for _, r := range rows {
		if strings.HasPrefix(r.Err, "no response within") {
			timeouts++
		}
		if strings.Contains(r.Err, "cancelled by the client") {
			t.Fatalf("a timeout was logged as the caller leaving: %+v", r)
		}
	}
	if timeouts == 0 {
		t.Fatalf("no timeout was logged: %+v", rows)
	}
}

// A preset advertises its whole pool, the free models xellter added included, not only the owner's list.
func TestModelsAdvertiseTheWholePool(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.get(t, "xl-direct", "/v1/models/text")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "over 1 models") {
		t.Fatalf("the preset lists only the owner's one model, not the free ones added to its pool: %s", body)
	}
}

// A restart must not forgive a model that was taken out a minute ago, and the panel keeps the history of who
// was taken out and why (ADR 0029).
func TestAModelThatIsOutStaysOutAfterARestartAndIsRecorded(t *testing.T) {
	e := setup(t, nil)
	now := time.Now()
	var why string
	for i := 0; i < 6; i++ {
		why, _ = e.a.Rt.Observe("babu", "text", "", "cheap-b", 20*time.Second, true, now.Add(time.Duration(i)*time.Second))
	}
	if why == "" {
		t.Fatal("six slow requests did not take the model out")
	}
	// the event is written off the request path
	var events []store.ModelEvent
	for i := 0; i < 50 && len(events) == 0; i++ {
		events = e.a.St.ModelEvents("babu", "text", 10)
		time.Sleep(20 * time.Millisecond)
	}
	if len(events) != 1 || events[0].Model != "cheap-b" || events[0].Kind != "slow" || events[0].Reason != why {
		t.Fatalf("events = %+v", events)
	}
	e.a.Close()

	a2 := reopen(t, e)
	defer a2.Close()
	out := a2.Rt.OutOfService("babu", "text", time.Now())
	if len(out) != 1 || out[0].Model != "cheap-b" || out[0].Reason != why {
		t.Fatalf("after the restart the model is out as %+v", out)
	}
}

func TestPresetStatsCountWhatTheHealthRulesLookAt(t *testing.T) {
	e := setup(t, nil)
	for i := 0; i < 3; i++ {
		resp, body := e.post(t, e.keyCl, "/v1/chat/completions", `{"model":"text","stream":true,"messages":[{"role":"user","content":"q`+string(rune('a'+i))+`"}]}`)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
	}
	e.a.St.Flush()
	stats := e.a.St.PresetStats("babu", "text", time.Now().Add(-time.Hour))
	n := 0
	for _, s := range stats {
		n += s.Requests
		if s.Failed != 0 {
			t.Fatalf("stats = %+v", s)
		}
	}
	if n != 3 {
		t.Fatalf("counted %d requests, want 3: %+v", n, stats)
	}
}

func TestHealthzReportsLostLogRows(t *testing.T) {
	e := setup(t, nil)
	resp, body := e.get(t, "", "/healthz")
	if resp.StatusCode != 200 || !strings.Contains(body, `"log_dropped":0`) || !strings.Contains(body, `"log_lost":0`) {
		t.Fatalf("healthz = %d %s", resp.StatusCode, body)
	}
}

// kenari's free models sometimes answer 200 with a message that says nothing. The caller cannot use it, so the
// request moves to the next model and the model is marked down (ADR 0046).
func twoModels(c *config.Config) {
	c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"free-a:free", "cheap-b", "pro-c"}}
}

// hit posts requests, one session each, until the model has been called n times, and returns the answers. The ring
// hands a fresh session to the next model, so a model is first in line only one request in a few.
func hit(t *testing.T, e *env, model string, n int, body string) []string {
	t.Helper()
	var served []string
	for i := 0; e.f.count(model) < n; i++ {
		if i > 60 {
			t.Fatalf("%s was never reached %d times", model, n)
		}
		resp, _ := e.post(t, e.keyCl, "/v1/chat/completions", body, "X-Xellter-Session", fmt.Sprint("hit", i))
		served = append(served, resp.Header.Get("X-Xellter-Model"))
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	return served
}

const emptyJSON = `{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":10,"completion_tokens":0}}`

func TestAnEmptyAnswerFailsOverAndEvictsAfterThree(t *testing.T) {
	e := setup(t, twoModels)
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			return 200, emptyJSON
		}
		return 0, ""
	}
	for _, m := range hit(t, e, "free-a:free", 3, fmt.Sprintf(chatBody, "text")) {
		if m == "free-a:free" || m == "" {
			t.Fatalf("an empty answer must move on to a model that answers, got %q", m)
		}
	}
	if n := e.f.count("free-a:free"); n != 3 {
		t.Fatalf("an empty answer is not retried on the same model: %d calls", n)
	}
	out := e.a.Rt.OutOfService("babu", "text", time.Now())
	if len(out) != 1 || out[0].Model != "free-a:free" || !strings.Contains(out[0].Reason, "empty answer") {
		t.Fatalf("the third empty answer must take the model out, saying why: %+v", out)
	}
}

// When no other model can be tried the empty answer is what the caller gets, as it did before.
func TestAnEmptyAnswerIsRelayedWhenNoModelIsLeft(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		c.Clients[0].Presets["text"] = config.Preset{Category: "chat", Optimization: "cheapest", Models: []string{"cheap-b"}}
	})
	e.f.chat = func(c call, n int) (int, string) { return 200, emptyJSON }
	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"))
	if resp.StatusCode != 200 || !strings.Contains(body, `"content":""`) {
		t.Fatalf("the last empty answer must still reach the caller: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Xellter-Model") == "" {
		t.Fatal("a relayed answer says which model gave it")
	}
}

// A stream that finishes with nothing in it has already been sent to the caller, so it cannot fail over, but it
// is counted against the model all the same.
func TestAnEmptyStreamIsCountedAgainstTheModel(t *testing.T) {
	e := setup(t, twoModels)
	stream := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			return 200, stream
		}
		return 0, ""
	}
	hit(t, e, "free-a:free", 5, `{"model":"text","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if out := e.a.Rt.OutOfService("babu", "text", time.Now()); len(out) != 1 || out[0].Model != "free-a:free" {
		t.Fatalf("five empty streams in a row must take the model out: %+v", out)
	}
}

// A non-streamed answer has no first token, so its wait is the whole answer less the time to write it. A model
// that makes every such caller wait is taken out like a slow stream.
func TestASlowNonStreamedModelIsTakenOut(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		twoModels(c)
		c.Routing.EvictWindowMin = 1 // as on the live server: the slow record must not depend on it
		c.Routing.SlowTTFTSecs = 1
		c.Routing.HealthMinReqs = 3
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			time.Sleep(1200 * time.Millisecond)
			return 200, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":0}}`
		}
		return 0, ""
	}
	hit(t, e, "free-a:free", 3, fmt.Sprintf(chatBody, "text"))
	out := e.a.Rt.OutOfService("babu", "text", time.Now())
	if len(out) != 1 || out[0].Model != "free-a:free" || out[0].Kind != "slow" {
		t.Fatalf("a model that queues non-streamed answers must go out: %+v", out)
	}
}

// A model that refuses every request while another model answers the same request is the one at fault, and
// goes out after a run of such refusals, however far apart they come.
func TestAModelThatAlwaysRefusesIsTakenOut(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		twoModels(c)
		c.Routing.EvictErrors = 1000 // leave the three-errors rule out of it
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			return 400, `{"error":{"code":"bad_request","message":"nope"}}`
		}
		return 0, ""
	}
	hit(t, e, "free-a:free", 4, fmt.Sprintf(chatBody, "text"))
	if out := e.a.Rt.OutOfService("babu", "text", time.Now()); len(out) != 0 {
		t.Fatalf("out after only four refusals: %+v", out)
	}
	hit(t, e, "free-a:free", 5, fmt.Sprintf(chatBody, "text"))
	out := e.a.Rt.OutOfService("babu", "text", time.Now())
	if len(out) != 1 || out[0].Model != "free-a:free" || out[0].Kind != "failing" {
		t.Fatalf("five refusals in a row must take the model out: %+v", out)
	}
}

// A request that every model refuses is a bad request, not five bad models.
func TestARequestEveryModelRefusesEvictsNobody(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		twoModels(c)
		c.Routing.EvictErrors = 1000
	})
	e.f.chat = func(c call, n int) (int, string) {
		return 400, `{"error":{"code":"bad_request","message":"messages is required"}}`
	}
	for i := 0; i < 8; i++ {
		e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"), "X-Xellter-Session", fmt.Sprint("s", i))
	}
	if out := e.a.Rt.OutOfService("babu", "text", time.Now()); len(out) != 0 {
		t.Fatalf("a request nobody could answer must not evict anyone: %+v", out)
	}
}

// A caller with a short timeout hangs up on a slow model before it answers. That is not the model's failure, but
// the wait is a slow answer, and without it the model would never look slow (ADR 0046).
func TestCallersThatGiveUpOnAModelMakeItSlow(t *testing.T) {
	e := setup(t, func(c *config.Config) {
		twoModels(c)
		c.Routing.SlowTTFTSecs = 1
		c.Routing.HealthMinReqs = 3
	})
	e.f.chat = func(c call, n int) (int, string) {
		if c.Model == "free-a:free" {
			time.Sleep(1500 * time.Millisecond)
			return 200, emptyJSON
		}
		return 0, ""
	}
	hc := &http.Client{Timeout: 1200 * time.Millisecond}
	for i := 0; e.f.count("free-a:free") < 3; i++ {
		if i > 60 {
			t.Fatal("free-a was not reached")
		}
		req, _ := http.NewRequest("POST", e.px.URL+"/v1/chat/completions", strings.NewReader(fmt.Sprintf(chatBody, "text")))
		req.Header.Set("Authorization", "Bearer "+e.keyCl)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Xellter-Session", fmt.Sprint("g", i))
		if resp, err := hc.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	time.Sleep(500 * time.Millisecond) // the server notices the hang-up after the caller does
	out := e.a.Rt.OutOfService("babu", "text", time.Now())
	if len(out) != 1 || out[0].Model != "free-a:free" || out[0].Kind != "slow" {
		t.Fatalf("callers that give up after the limit must make the model slow: %+v", out)
	}
}
