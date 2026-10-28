package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/config"
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
