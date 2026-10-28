package app_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/axellageraldinc/xellter/internal/config"
)

// localServer is a fake OpenAI-compatible transcription server. It records what it was sent.
type localServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	hits   int
	auth   string
	apiKey string
	body   string
	status int // 0 = 200
}

func newLocalServer(t *testing.T) *localServer {
	l := &localServer{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		l.mu.Lock()
		l.hits++
		l.auth, l.apiKey, l.body = r.Header.Get("Authorization"), r.Header.Get("x-api-key"), string(b)
		st := l.status
		l.mu.Unlock()
		if st != 0 {
			w.WriteHeader(st)
			return
		}
		io.WriteString(w, `{"text":"halo dari lokal"}`)
	}))
	t.Cleanup(l.srv.Close)
	return l
}

func transcribe(t *testing.T, e *env, model string) (*http.Response, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", model)
	fw, _ := mw.CreateFormFile("file", "a.mp3")
	fw.Write([]byte("AUDIO"))
	mw.Close()
	req, _ := http.NewRequest("POST", e.px.URL+"/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+e.keyCl)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, string(b)
}

func withLocal(l *localServer) func(*config.Config) {
	return func(c *config.Config) {
		c.LocalModels = []config.LocalModel{{ID: "maleo-small-id", Category: "audio_transcription", BaseURL: l.srv.URL, Model: "small-id"}}
	}
}

// A local model joins every STT preset by itself, is served without a kenari key, and is asked for by the
// name its own server knows (ADR 0033).
func TestLocalModelJoinsSTTPresetAndGetsNoKenariKey(t *testing.T) {
	l := newLocalServer(t)
	e := setup(t, withLocal(l))
	before := e.f.count("stt-1")
	r, body := transcribe(t, e, "stt")
	if r.StatusCode != 200 || r.Header.Get("X-Xellter-Model") != "maleo-small-id" || !strings.Contains(body, "halo dari lokal") {
		t.Fatalf("the free local model should lead a cheapest STT preset: %d %s %q", r.StatusCode, body, r.Header.Get("X-Xellter-Model"))
	}
	if l.hits != 1 || !strings.Contains(l.body, "small-id") || !strings.Contains(l.body, "AUDIO") || strings.Contains(l.body, "maleo-small-id") {
		t.Fatalf("the local server must see its own model name and the audio: hits=%d body=%q", l.hits, l.body)
	}
	if l.auth != "" || l.apiKey != "" {
		t.Fatalf("a kenari key reached the local server: %q %q", l.auth, l.apiKey)
	}
	if e.f.count("stt-1") != before {
		t.Fatal("kenari was called for a request the local model served")
	}
}

// A local server that is down does not fail the request: the next model of the pool takes it.
func TestLocalModelFailureFallsBackToKenari(t *testing.T) {
	l := newLocalServer(t)
	l.status = 503
	e := setup(t, withLocal(l))
	r, body := transcribe(t, e, "stt")
	if r.StatusCode != 200 || r.Header.Get("X-Xellter-Model") != "stt-1" {
		t.Fatalf("expected the kenari model after the local one failed: %d %s %q", r.StatusCode, body, r.Header.Get("X-Xellter-Model"))
	}
}

// A kenari model with the same id wins: a local entry never shadows it.
func TestLocalModelNeverShadowsKenari(t *testing.T) {
	l := newLocalServer(t)
	e := setup(t, func(c *config.Config) {
		c.LocalModels = []config.LocalModel{{ID: "stt-1", Category: "audio_transcription", BaseURL: l.srv.URL}}
	})
	if r, body := transcribe(t, e, "stt"); r.StatusCode != 200 || l.hits != 0 {
		t.Fatalf("kenari's stt-1 must be used: %d %s hits=%d", r.StatusCode, body, l.hits)
	}
}

func TestLocalModelConfigIsValidated(t *testing.T) {
	for name, lm := range map[string]config.LocalModel{
		"bad id":       {ID: "a b", Category: "audio_transcription", BaseURL: "http://127.0.0.1:1"},
		"chat":         {ID: "x", Category: "chat", BaseURL: "http://127.0.0.1:1"},
		"no url":       {ID: "x", Category: "audio_transcription"},
		"not http url": {ID: "x", Category: "audio_transcription", BaseURL: "file:///etc"},
	} {
		c := config.Default()
		c.LocalModels = []config.LocalModel{lm}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
	c := config.Default()
	c.LocalModels = []config.LocalModel{{ID: "x", Category: "audio_transcription", BaseURL: "http://127.0.0.1:8490"}, {ID: "x", Category: "audio_transcription", BaseURL: "http://127.0.0.1:8491"}}
	if err := c.Validate(); err == nil {
		t.Error("a duplicate id must be refused")
	}
}

func get(t *testing.T, e *env, path string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", e.px.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+e.keyCl)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, r.StatusCode, b)
	}
	return string(b)
}

// Traffic served locally is usage like any other, in the numbers a client reads (ADR 0033).
func TestLocalModelShowsInClientUsage(t *testing.T) {
	l := newLocalServer(t)
	e := setup(t, withLocal(l))
	for i := 0; i < 2; i++ {
		if r, body := transcribe(t, e, "stt"); r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, body)
		}
	}
	e.a.St.Flush()
	for _, p := range []string{"/v1/usage?range=24h", "/v1/usage/models?range=24h"} {
		if b := get(t, e, p); !strings.Contains(b, `"maleo-small-id"`) {
			t.Errorf("%s does not list the local model: %s", p, b)
		}
	}
	if b := get(t, e, "/v1/usage?range=24h"); !strings.Contains(b, `"billing_kind":"free"`) {
		t.Errorf("a local model is billed as free: %s", b)
	}
	if b := get(t, e, "/v1/models"); !strings.Contains(b, `"stt"`) {
		t.Errorf("the client's preset list: %s", b)
	}
}
