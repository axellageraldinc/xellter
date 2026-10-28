package app_test

import (
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
