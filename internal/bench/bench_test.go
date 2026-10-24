package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		got[s.Model] = s
	}
	if g := got["claude-opus-4-7"]; g.Matched != "Claude Opus 4.7" || g.Rating != 1483 {
		t.Fatalf("the most voted variant should win: %+v", g)
	}
	if _, ok := got["muse-wonder-1-0"]; !ok {
		t.Fatal("a trailing .0 release number must not block a match")
	}
	if _, ok := got["weak-model:free"]; !ok {
		t.Fatal("the :free suffix must be ignored")
	}
	if _, ok := got["nobody"]; ok {
		t.Fatal("unknown models stay unscored")
	}
	if _, ok := got["nano-banana-pro"]; !ok {
		t.Fatal("aliases in parentheses must match")
	}
	if _, ok := got["claude-opus-4-7-as-image"]; ok {
		t.Fatal("a text model must not match an image arena entry")
	}
	if v := got["muse-wonder-1-0"].Value; v < 0.49 || v > 0.51 { // Muse Wonder is the median of the five text rows
		t.Fatalf("the median model scores about 0.5: %v", v)
	}
	if !(got["claude-opus-4-7"].Value > got["muse-wonder-1-0"].Value && got["muse-wonder-1-0"].Value > got["mid-model"].Value && got["mid-model"].Value > got["weak-model:free"].Value) {
		t.Fatalf("scores must follow ratings: %+v", got)
	}
	// a wide interval costs points: same rating, fewer votes
	wide := Match([]Row{{Arena: "text", Name: "Model Aaa", Rating: 1500, Lo: 1400, Hi: 1600}, {Arena: "text", Name: "Model Bbb", Rating: 1500, Lo: 1495, Hi: 1505}, {Arena: "text", Name: "Model Mid", Rating: 1300}},
		map[string]*catalog.Model{"model-aaa": {ID: "model-aaa", Endpoint: "chat"}, "model-bbb": {ID: "model-bbb", Endpoint: "chat"}})
	if len(wide) != 2 || wide[0].Value >= wide[1].Value {
		t.Fatalf("the well measured model must score above the uncertain one: %+v", wide)
	}
	if v := got["claude-opus-4-7"].Value; v < 0.6 || v > 0.65 { // 83 rating points above the median: 1/(1+10^(-83/400)) = 0.62
		t.Fatalf("expected a win probability of about 0.62, got %v", v)
	}
}

func fakeDataset(t *testing.T, fail502Once bool) *httptest.Server {
	t.Helper()
	failed := false
	mk := func(cat, name string, rating float64) map[string]any {
		return map[string]any{"row": map[string]any{"model_name": name, "rating": rating, "rating_lower": rating - 5, "rating_upper": rating + 5, "vote_count": 100, "category": cat, "leaderboard_publish_date": "2026-10-22"}}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail502Once && !failed {
			failed = true
			w.WriteHeader(502)
			return
		}
		q := r.URL.Query()
		if q.Get("dataset") != "lmarena-ai/leaderboard-dataset" || q.Get("split") != "latest" {
			t.Errorf("unexpected query %v", q)
		}
		off := 0
		fmt.Sscan(q.Get("offset"), &off)
		var rows []map[string]any
		switch q.Get("config") {
		case "text":
			rows = []map[string]any{mk("overall", "Model A", 1500), mk("overall", "Model B", 1400), mk("coding", "Model A", 1600)}
		case "text_to_image":
			rows = []map[string]any{mk("overall", "img-a", 1300)}
		default:
			rows = nil
		}
		if off > 0 {
			rows = nil
		}
		json.NewEncoder(w).Encode(map[string]any{"rows": rows, "num_rows_total": len(rows)})
	}))
}

func TestFetchTakesOnlyTheOverallCategoryAndRetriesServerErrors(t *testing.T) {
	srv := fakeDataset(t, true)
	defer srv.Close()
	rows, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Arena+":"+r.Name)
	}
	if strings.Join(names, ",") != "text:Model A,text:Model B,text_to_image:img-a" {
		t.Fatalf("rows: %v", names)
	}
}
