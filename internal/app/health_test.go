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
