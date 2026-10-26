package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
)

func has(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func TestKeysStripRunSettingsAndKeepAliases(t *testing.T) {
	cases := map[string]string{ // leaderboard name -> a key it must produce
		"DeepSeek-V4.1-Flash-20260910 (Max)":        "deepseek-v4-1-flash",
		"Gemini 3.1 Pro Preview":                    "gemini-3-1-pro",
		"NVIDIA-Nemotron-3-Ultra-550B-A55B (NVFP4)": "nemotron-3-ultra-550b-a55b",
		"gemini-3-pro-image-2k (nano-banana-pro)":   "nano-banana-pro",
		"dreamina-seedance-2.0-720p":                "seedance-2-0",
		"Claude Opus 4.7 (High)":                    "claude-opus-4-7",
		"Gemma 3 27B IT":                            "gemma-3-27b",
		"gpt-image-2.5-sunburst":                    "gpt-image-2-5-sunburst",
		"Kimi K2.5 (Thinking)":                      "kimi-k2-5",
	}
	for name, want := range cases {
		if got := Keys(name); !has(got, want) {
			t.Errorf("%q -> %v, want %q", name, got, want)
		}
	}
	if k := Keys("Something (High)"); has(k, "high") {
		t.Errorf("effort settings are not aliases: %v", k)
	}
}

func TestMatchUsesTheMostVotedVariantAndAWinProbability(t *testing.T) {
	rows := []Row{
		{Arena: "text", Name: "Claude Opus 4.7 (High)", Rating: 1490, Votes: 9000, Date: "2026-10-22"},
		{Arena: "text", Name: "Claude Opus 4.7", Rating: 1483, Lo: 1478, Hi: 1488, Votes: 12000, Date: "2026-10-22"},
		{Arena: "text", Name: "Muse Wonder (Max)", Rating: 1400, Votes: 3000, Date: "2026-10-22"},
		{Arena: "text", Name: "Mid Model", Rating: 1200, Votes: 8000, Date: "2026-10-22"},
		{Arena: "text", Name: "Weak Model", Rating: 900, Votes: 8000, Date: "2026-10-22"},
		{Arena: "text_to_image", Name: "gemini-3-pro-image (nano-banana-pro)", Rating: 1300, Votes: 100, Date: "2026-10-07"},
		{Arena: "text_to_image", Name: "other-image", Rating: 1100, Votes: 100, Date: "2026-10-07"},
		{Arena: "text_to_image", Name: "third-image", Rating: 1000, Votes: 100, Date: "2026-10-07"},
	}
	models := map[string]*catalog.Model{
		"claude-opus-4-7": {ID: "claude-opus-4-7", Endpoint: "chat"}, "muse-wonder-1-0": {ID: "muse-wonder-1-0", Endpoint: "chat"},
		"mid-model": {ID: "mid-model", Endpoint: "chat"}, "weak-model:free": {ID: "weak-model:free", Endpoint: "chat"}, "nobody": {ID: "nobody", Endpoint: "chat"},
		"nano-banana-pro":          {ID: "nano-banana-pro", Endpoint: "images"},
		"claude-opus-4-7-as-image": {ID: "claude-opus-4-7-as-image", Endpoint: "images"},
	}
	got := map[string]Score{}
	for _, s := range Match(rows, models) {
		got[s.Model+"|"+s.Arena] = s
	}
	if g := got["claude-opus-4-7|text"]; g.Matched != "Claude Opus 4.7" || g.Rating != 1483 {
		t.Fatalf("the most voted variant should win: %+v", g)
	}
	if _, ok := got["muse-wonder-1-0|text"]; !ok {
		t.Fatal("a trailing .0 release number must not block a match")
	}
	if _, ok := got["weak-model:free|text"]; !ok {
		t.Fatal("the :free suffix must be ignored")
	}
	if _, ok := got["nobody|text"]; ok {
		t.Fatal("unknown models stay unscored")
	}
	if _, ok := got["nano-banana-pro|text_to_image"]; !ok {
		t.Fatal("aliases in parentheses must match, and only on the arena that ranks images")
	}
	if _, ok := got["claude-opus-4-7-as-image|text_to_image"]; ok {
		t.Fatal("a text model must not match an image arena entry")
	}
	if v := got["muse-wonder-1-0|text"].Value; v < 0.49 || v > 0.51 { // Muse Wonder is the median of the five text rows
		t.Fatalf("the median model scores about 0.5: %v", v)
	}
	if !(got["claude-opus-4-7|text"].Value > got["muse-wonder-1-0|text"].Value && got["muse-wonder-1-0|text"].Value > got["mid-model|text"].Value && got["mid-model|text"].Value > got["weak-model:free|text"].Value) {
		t.Fatalf("scores must follow ratings: %+v", got)
	}
	// a wide interval costs points: same rating, fewer votes
	wide := Match([]Row{{Arena: "text", Name: "Model Aaa", Rating: 1500, Lo: 1400, Hi: 1600}, {Arena: "text", Name: "Model Bbb", Rating: 1500, Lo: 1495, Hi: 1505}, {Arena: "text", Name: "Model Mid", Rating: 1300}},
		map[string]*catalog.Model{"model-aaa": {ID: "model-aaa", Endpoint: "chat"}, "model-bbb": {ID: "model-bbb", Endpoint: "chat"}})
	if len(wide) != 2 || wide[0].Value >= wide[1].Value {
		t.Fatalf("the well measured model must score above the uncertain one: %+v", wide)
	}
	if v := got["claude-opus-4-7|text"].Value; v < 0.6 || v > 0.65 { // 83 rating points above the median: 1/(1+10^(-83/400)) = 0.62
		t.Fatalf("expected a win probability of about 0.62, got %v", v)
	}
}

// SWE-bench, Aider and the ASR leaderboard publish a score instead of a rating, so their values are used as
// they are (already 0..1) and each keeps its own arena name.
func TestMatchKeepsPublishedScoresAsTheyAre(t *testing.T) {
	rows := []Row{
		{Arena: "swebench", Name: "Claude Opus 4.7", Rating: 72, Value: 0.72, Ranked: true},
		{Arena: "aider", Name: "Claude Opus 4.7", Rating: 80, Value: 0.8, Ranked: true},
		{Arena: "asr", Name: "Whisper Large V3", Rating: 7.4, Value: 1, Ranked: true},
	}
	models := map[string]*catalog.Model{
		"claude-opus-4-7":    {ID: "claude-opus-4-7", Endpoint: "chat"},
		"whisper-large-v3":   {ID: "whisper-large-v3", Endpoint: "audio_transcription"},
		"claude-opus-4-7-v2": {ID: "claude-opus-4-7-v2", Endpoint: "chat"},
	}
	got := map[string]Score{}
	for _, s := range Match(rows, models) {
		got[s.Model+"|"+s.Arena] = s
	}
	if v := got["claude-opus-4-7|swebench"].Value; v != 0.72 {
		t.Fatalf("a published percentage must not be converted: %v", v)
	}
	if v := got["claude-opus-4-7|aider"].Value; v != 0.8 {
		t.Fatalf("aider: %v", v)
	}
	if v := got["whisper-large-v3|asr"].Value; v != 1 {
		t.Fatalf("asr: %v", v)
	}
	// a coding score never lands on an image model, and an ASR score never lands on a chat model
	if _, ok := got["claude-opus-4-7-v2|swebench"]; ok {
		t.Fatal("swebench applies to chat models only")
	}
}

func fakeDataset(t *testing.T, fail502Once bool) *httptest.Server {
	t.Helper()
	failed := false
	mk := func(cat, name string, rating float64) map[string]any {
		return map[string]any{"row": map[string]any{"model_name": name, "rating": rating, "rating_lower": rating - 5, "rating_upper": rating + 5, "vote_count": 100, "category": cat, "leaderboard_publish_date": "2026-10-22"}}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/swe", "/aider": // served, but without the payload those sources need
			fmt.Fprint(w, "nothing here")
			return
		case "/rows":
		default:
			w.WriteHeader(404)
			return
		}
		if fail502Once && !failed {
			failed = true
			w.WriteHeader(502)
			return
		}
		q := r.URL.Query()
		if q.Get("dataset") == "lmarena-ai/leaderboard-dataset" && q.Get("split") != "latest" {
			t.Errorf("unexpected query %v", q)
		}
		switch q.Get("dataset") {
		case "lmarena-ai/leaderboard-dataset":
			off := 0
			fmt.Sscan(q.Get("offset"), &off)
			var rows []map[string]any
			switch q.Get("config") {
			case "text":
				rows = []map[string]any{mk("overall", "Model A", 1500), mk("overall", "Model B", 1400), mk("coding", "Model A", 1600)}
			case "text_to_image":
				rows = []map[string]any{mk("overall", "img-a", 1300)}
			}
			if off > 0 {
				rows = nil
			}
			json.NewEncoder(w).Encode(map[string]any{"rows": rows, "num_rows_total": len(rows)})
		case "reach-vb/open-asr-leaderboard-evals-all":
			json.NewEncoder(w).Encode(map[string]any{"rows": []map[string]any{
				{"row": map[string]any{"Model name": "whisper-a", "Avg. WER": 5.0}},
				{"row": map[string]any{"Model name": "whisper-b", "Avg. WER": 15.0}},
			}})
		default:
			t.Errorf("unexpected dataset %q", q.Get("dataset"))
			w.WriteHeader(404)
		}
	}))
}

// fetchArena is exercised through Fetch: it must keep the overall category only, retry a server error, and
// carry on when one source is missing.
func TestFetchTakesOnlyTheOverallCategoryAndRetriesServerErrors(t *testing.T) {
	srv := fakeDataset(t, true)
	defer srv.Close()
	rows, err := Fetch(context.Background(), srv.Client(), Sources{Base: srv.URL, SWE: srv.URL + "/swe", Aider: srv.URL + "/aider"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Arena+":"+r.Name)
	}
	if strings.Join(names, ",") != "text:Model A,text:Model B,text_to_image:img-a,asr:whisper-a,asr:whisper-b" {
		t.Fatalf("rows: %v", names)
	}
	// the ASR values are inverted and scaled to 0..1: the best model is 1, the worst 0
	for _, r := range rows {
		if r.Arena != "asr" {
			continue
		}
		if r.Name == "whisper-a" && r.Value != 1 {
			t.Fatalf("the best WER must become 1: %+v", r)
		}
		if r.Name == "whisper-b" && r.Value != 0 {
			t.Fatalf("the worst WER must become 0: %+v", r)
		}
	}
}

// When every source fails there is nothing to report but the error.
func TestFetchFailsWhenEverySourceFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	// the fetch retries a failing source with a growing pause; the deadline stops the wait
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if rows, err := Fetch(ctx, srv.Client(), Sources{Base: srv.URL, SWE: srv.URL + "/swe", Aider: srv.URL + "/aider"}); err == nil || len(rows) != 0 {
		t.Fatalf("expected an error, got %d rows", len(rows))
	}
}

// SWE-bench is scraped from the leaderboard page's embedded payload, and Aider from its YAML data file.
func TestFetchReadsSWEAndAider(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/swe", func(w http.ResponseWriter, r *http.Request) {
		io := `<!doctype html><html><script>var data = [{"model_display":"Claude Opus 4.7","resolved":72.5,"date":"2026-10-01","checked":true},{"model_display":"Other","resolved":50,"date":"2026-10-01"}];var per_instance = {"per_instance_details":{}};</script></html>`
		fmt.Fprint(w, io)
	})
	mux.HandleFunc("/aider", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `- model: Claude Opus 4.7
  pass_rate_2: 80.0
  dirname: 2026-10-01
- model: Claude Opus 4.7
  pass_rate_2: 70.0
  dirname: 2026-09-01
`)
	})
	mux.HandleFunc("/rows", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("dataset") {
		case "lmarena-ai/leaderboard-dataset": // no Arena rows, so the two scraped sources are what the test reads
			json.NewEncoder(w).Encode(map[string]any{"rows": []map[string]any{}})
		case "reach-vb/open-asr-leaderboard-evals-all":
			json.NewEncoder(w).Encode(map[string]any{"rows": []map[string]any{
				{"row": map[string]any{"Model name": "whisper-a", "Avg. WER": 5.0}},
			}})
		default:
			w.WriteHeader(404)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	rows, err := Fetch(context.Background(), srv.Client(), Sources{Base: srv.URL, SWE: srv.URL + "/swe", Aider: srv.URL + "/aider"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Row{}
	for _, r := range rows {
		got[r.Arena+":"+r.Name] = r
	}
	if r := got["swebench:Claude Opus 4.7"]; r.Value != 0.725 {
		t.Fatalf("swebench resolved percentage: %+v", r)
	}
	// the best pass rate of a model wins
	if r := got["aider:Claude Opus 4.7"]; r.Value != 0.8 {
		t.Fatalf("aider pass rate: %+v", r)
	}
}

// A source that fails must not lose the others: partial quality evidence is better than none.
func TestFetchKeepsTheSourcesThatWork(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rows", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("dataset") == "lmarena-ai/leaderboard-dataset" {
			json.NewEncoder(w).Encode(map[string]any{"rows": []map[string]any{
				{"row": map[string]any{"model_name": "Model A", "rating": 1500, "rating_lower": 1495, "rating_upper": 1505, "vote_count": 100, "category": "overall", "leaderboard_publish_date": "2026-10-22"}},
			}, "num_rows_total": 1})
			return
		}
		w.WriteHeader(404)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	rows, err := Fetch(ctx, srv.Client(), Sources{Base: srv.URL, SWE: srv.URL + "/nope", Aider: srv.URL + "/nope"})
	if err != nil {
		t.Fatalf("a failing source must not fail the whole fetch: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("the Arena rows must survive")
	}
}
