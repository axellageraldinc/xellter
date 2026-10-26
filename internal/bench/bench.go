// Package bench downloads public quality leaderboards and maps them onto kenari model ids.
//
// Sources, all free and keyless:
//   - the Arena (LMArena) leaderboard dataset on Hugging Face, which publishes human-preference ratings
//     for text, web development, agents, vision, documents, text-to-image and text-to-video. A rating is
//     turned into the probability that the model beats the arena's median model in a head-to-head vote.
//   - SWE-bench Verified, from the leaderboard page's embedded submission payload: a percentage of resolved
//     issues, which needs no interpretation.
//   - the Aider polyglot leaderboard, from the repository's YAML data file: a pass rate on coding exercises.
//   - the Open ASR Leaderboard on Hugging Face: word error rate, where lower is better, so it is inverted.
//
// Every source is normalised to 0..1 with higher meaning better, so the ranking engine can mix them.
// Nothing here is a model call and nothing is scored by a model: these are published measurements.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
)

// DefaultBase is the Hugging Face datasets server.
const DefaultBase = "https://datasets-server.huggingface.co"

// Defaults for the two non-Arena sources, so tests can point them at a fake.
const (
	DefaultSWEURL   = "https://www.swebench.com/"
	DefaultAiderURL = "https://raw.githubusercontent.com/Aider-AI/aider/main/aider/website/_data/polyglot_leaderboard.yml"
)

const dataset = "lmarena-ai/leaderboard-dataset"

// Arenas maps an Arena config to the catalog endpoint it ranks. The client purpose decides which of these
// the text ranking uses (see rank.purposeArenas).
var Arenas = []struct{ Config, Endpoint string }{
	{"text", "chat"},
	{"webdev", "chat"},
	{"agent", "chat"},
	{"vision", "chat"},
	{"document", "chat"},
	{"text_to_image", "images"},
	{"text_to_video", "videos"},
}

// Row is one leaderboard entry as published.
type Row struct {
	Arena  string  `json:"arena"`
	Name   string  `json:"name"`
	Rating float64 `json:"rating"`
	Lo     float64 `json:"lo"`
	Hi     float64 `json:"hi"`
	Votes  float64 `json:"votes"`
	Date   string  `json:"date"`
	// Value is set instead of Rating when the source publishes a score rather than a rating (ASR WER).
	Value float64 `json:"value,omitempty"`
	// Ranked marks a source whose value is already 0..1 (no Bradley-Terry conversion).
	Ranked bool `json:"-"`
}

// Score is a leaderboard entry mapped onto one catalog model.
type Score struct {
	Model   string  `json:"model"`
	Arena   string  `json:"arena"`
	Value   float64 `json:"value"` // 0..1, higher is better
	Rating  float64 `json:"rating"`
	Lo      float64 `json:"lo"`
	Hi      float64 `json:"hi"`
	Matched string  `json:"matched"` // the leaderboard name that was matched
	Date    string  `json:"date"`
	Votes   float64 `json:"votes"`
}

// Sources are the endpoints Fetch talks to. Zero values fall back to the defaults.
type Sources struct {
	Base      string // Hugging Face datasets server
	SWE       string // SWE-bench leaderboard page
	Aider     string // Aider polyglot leaderboard YAML
	SkipArena bool   // tests: only the non-Arena sources
}

// Fetch downloads every source. A failing source is reported but does not lose the others: partial quality
// evidence is better than none, and the ranking engine labels what it used.
func Fetch(ctx context.Context, hc *http.Client, src Sources) ([]Row, error) {
	if src.Base == "" {
		src.Base = DefaultBase
	}
	if src.SWE == "" {
		src.SWE = DefaultSWEURL
	}
	if src.Aider == "" {
		src.Aider = DefaultAiderURL
	}
	var all []Row
	var firstErr error
	keep := func(rows []Row, err error, what string) {
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", what, err)
			}
			return
		}
		all = append(all, rows...)
	}
	if !src.SkipArena {
		for _, a := range Arenas {
			rows, err := fetchArena(ctx, hc, src.Base, a.Config)
			keep(rows, err, a.Config)
		}
	}
	swe, err := fetchSWE(ctx, hc, src.SWE)
	keep(swe, err, "swebench")
	aid, err := fetchAider(ctx, hc, src.Aider)
	keep(aid, err, "aider")
	asr, err := fetchASR(ctx, hc, src.Base)
	keep(asr, err, "asr")
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

func get(ctx context.Context, hc *http.Client, u string, out any) error {
	var last error
	for i := range 4 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "xellter/1.0")
		resp, err := hc.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
			resp.Body.Close()
			if err == nil {
				return nil
			}
		} else if err == nil {
			resp.Body.Close()
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		last = err
		select { // the datasets server answers 500/502 now and then
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 1500 * time.Millisecond):
		}
	}
	return last
}

func getText(ctx context.Context, hc *http.Client, u string) ([]byte, error) {
	var last error
	for i := range 3 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "xellter/1.0")
		resp, err := hc.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			if err == nil {
				return b, nil
			}
		} else if err == nil {
			resp.Body.Close()
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(i+1) * time.Second):
		}
	}
	return nil, last
}

// fetchArena downloads one Arena leaderboard's "overall" category. The dataset lists the overall category
// first, so paging stops at the first row of another category.
func fetchArena(ctx context.Context, hc *http.Client, base, cfg string) ([]Row, error) {
	var rows []Row
	for off := 0; off < 3000; off += 100 {
		q := url.Values{"dataset": {dataset}, "config": {cfg}, "split": {"latest"}, "offset": {fmt.Sprint(off)}, "length": {"100"}}
		var page struct {
			Rows []struct {
				Row struct {
					Name   string  `json:"model_name"`
					Rating float64 `json:"rating"`
					Lo     float64 `json:"rating_lower"`
					Hi     float64 `json:"rating_upper"`
					Votes  float64 `json:"vote_count"`
					Cat    string  `json:"category"`
					Date   string  `json:"leaderboard_publish_date"`
				} `json:"row"`
			} `json:"rows"`
			Total int `json:"num_rows_total"`
		}
		if err := get(ctx, hc, base+"/rows?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		for _, r := range page.Rows {
			if r.Row.Cat != "overall" {
				return rows, nil
			}
			rows = append(rows, Row{Arena: cfg, Name: r.Row.Name, Rating: r.Row.Rating, Lo: r.Row.Lo, Hi: r.Row.Hi, Votes: r.Row.Votes, Date: r.Row.Date})
		}
		if off+100 >= page.Total || len(page.Rows) == 0 {
			break
		}
	}
	return rows, nil
}

// fetchSWE reads SWE-bench Verified's published resolved percentage per submission from the leaderboard page.
// The page embeds its data as JSON, which is what this parses: no rendering, no browser.
func fetchSWE(ctx context.Context, hc *http.Client, u string) ([]Row, error) {
	b, err := getText(ctx, hc, u)
	if err != nil {
		return nil, err
	}
	page := string(b)
	i := strings.Index(page, `"per_instance_details"`)
	if i < 0 {
		return nil, fmt.Errorf("no leaderboard payload")
	}
	start := strings.LastIndex(page[:i], `[{"`)
	if start < 0 {
		return nil, fmt.Errorf("no leaderboard payload start")
	}
	var subs []struct {
		Model    string  `json:"model_display"`
		Resolved float64 `json:"resolved"`
		Date     string  `json:"date"`
		Checked  bool    `json:"checked"`
	}
	dec := json.NewDecoder(strings.NewReader(page[start:]))
	if err := dec.Decode(&subs); err != nil {
		return nil, fmt.Errorf("decode submissions: %w", err)
	}
	var rows []Row
	for _, s := range subs {
		if s.Model == "" || s.Resolved <= 0 {
			continue
		}
		rows = append(rows, Row{Arena: "swebench", Name: s.Model, Rating: s.Resolved, Value: s.Resolved / 100, Date: s.Date, Ranked: true})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no submissions")
	}
	return rows, nil
}

// fetchAider reads the polyglot leaderboard YAML, keeping the best pass rate per model.
func fetchAider(ctx context.Context, hc *http.Client, u string) ([]Row, error) {
	b, err := getText(ctx, hc, u)
	if err != nil {
		return nil, err
	}
	type entry struct {
		Model     string  `yaml:"model"`
		PassRate  float64 `yaml:"pass_rate_2"`
		PassRate1 float64 `yaml:"pass_rate_1"`
		Date      string  `yaml:"dirname"`
	}
	var entries []entry
	if err := yamlUnmarshal(b, &entries); err != nil {
		return nil, err
	}
	best := map[string]Row{}
	for _, e := range entries {
		if e.Model == "" {
			continue
		}
		v := e.PassRate
		if v == 0 {
			v = e.PassRate1
		}
		if v <= 0 {
			continue
		}
		if old, ok := best[e.Model]; ok && old.Value >= v/100 {
			continue
		}
		best[e.Model] = Row{Arena: "aider", Name: e.Model, Rating: v, Value: v / 100, Date: e.Date, Ranked: true}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("no aider entries")
	}
	rows := make([]Row, 0, len(best))
	for _, r := range best {
		rows = append(rows, r)
	}
	return rows, nil
}

// fetchASR reads the Open ASR Leaderboard's aggregate word error rate per model and inverts it: the best
// model in the list becomes 1, the worst 0. Lower WER is better, so the scale has to be flipped.
func fetchASR(ctx context.Context, hc *http.Client, base string) ([]Row, error) {
	q := url.Values{"dataset": {"reach-vb/open-asr-leaderboard-evals-all"}, "config": {"default"}, "split": {"train"}, "offset": {"0"}, "length": {"100"}}
	var page struct {
		Rows []struct {
			Row map[string]any `json:"row"`
		} `json:"rows"`
	}
	if err := get(ctx, hc, base+"/rows?"+q.Encode(), &page); err != nil {
		return nil, err
	}
	type wer struct {
		name string
		v    float64
	}
	var list []wer
	for _, r := range page.Rows {
		name, _ := r.Row["Model name"].(string)
		v, _ := r.Row["Avg. WER"].(float64)
		if name == "" || v <= 0 {
			continue
		}
		list = append(list, wer{name, v})
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no ASR rows")
	}
	best, worst := list[0].v, list[0].v
	for _, x := range list {
		best = math.Min(best, x.v)
		worst = math.Max(worst, x.v)
	}
	var rows []Row
	for _, x := range list {
		v := 1.0
		if worst > best {
			v = 1 - (x.v-best)/(worst-best)
		}
		rows = append(rows, Row{Arena: "asr", Name: x.name, Rating: x.v, Value: v, Ranked: true})
	}
	return rows, nil
}

// ---- matching leaderboard names to catalog ids ----

var (
	parenRe = regexp.MustCompile(`[\(\[]([^\)\]]*)[\)\]]`)
	nonAlnu = regexp.MustCompile(`[^a-z0-9]+`)
	// tokens that describe how a model was run or packaged, not which model it is
	dropTok = map[string]bool{"preview": true, "instruct": true, "it": true, "nvfp4": true, "bf16": true, "fp8": true, "thinking": true,
		"nonthinking": true, "non": true, "instant": true, "max": true, "xhigh": true, "high": true, "medium": true, "low": true, "audio": true,
		"720p": true, "1080p": true, "480p": true, "2k": true, "4k": true, "nvidia": true, "dreamina": true, "web": true, "search": true, "free": true}
	dateTok = regexp.MustCompile(`^(19|20)\d{6}$`)
)

func tokens(s string) []string {
	s = strings.ToLower(strings.ReplaceAll(s, ":free", ""))
	var out []string
	for _, t := range strings.Split(nonAlnu.ReplaceAllString(s, "-"), "-") {
		if t == "" || dropTok[t] || dateTok.MatchString(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Keys returns the normalised names a leaderboard entry may be known by: its own name without run
// settings, and any alias written in parentheses ("gemini-3-pro-image (nano-banana-pro)").
func Keys(name string) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(ts []string) {
		if len(ts) == 0 {
			return
		}
		k := strings.Join(ts, "-")
		if len(k) >= 4 && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	base := parenRe.ReplaceAllString(name, " ")
	add(tokens(base))
	for _, m := range parenRe.FindAllStringSubmatch(name, -1) {
		if strings.ContainsAny(m[1], "-") || strings.Contains(m[1], " ") {
			add(tokens(m[1]))
		}
	}
	return keys
}

// catalogKeys are the keys a catalog id is looked up by: its normalised name, then the same without a trailing ".0"
// release number ("muse-wonder-1-0" is listed as "Muse Wonder"), then without a trailing size marker
// ("whisper-large-v3-turbo" is listed as "openai/whisper-large-v3").
func catalogKeys(id string) []string {
	ts := tokens(id)
	if len(ts) == 0 {
		return nil
	}
	keys := []string{strings.Join(ts, "-")}
	if n := len(ts); n > 2 && ts[n-1] == "0" && isDigits(ts[n-2]) {
		keys = append(keys, strings.Join(ts[:n-2], "-"))
	}
	if n := len(ts); n > 3 && ts[n-1] == "turbo" {
		keys = append(keys, strings.Join(ts[:n-1], "-"))
	}
	return keys
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Match maps leaderboard rows onto catalog models. When several entries share a key (one model measured at
// different reasoning settings) the one with the most votes wins.
func Match(rows []Row, models map[string]*catalog.Model) []Score {
	type arena struct {
		by  map[string]Row
		med float64
		raw bool // the source publishes 0..1 values already
	}
	arenas := map[string]*arena{}
	for _, r := range rows {
		ar := arenas[r.Arena]
		if ar == nil {
			ar = &arena{by: map[string]Row{}, raw: r.Ranked}
			arenas[r.Arena] = ar
		}
		if old, ok := ar.by[r.Name]; !ok || r.Votes > old.Votes {
			ar.by[r.Name] = r
		}
	}
	for name, ar := range arenas {
		if ar.raw {
			continue
		}
		rs := make([]Row, 0, len(ar.by))
		for _, r := range ar.by {
			rs = append(rs, r)
		}
		ratings := make([]float64, len(rs))
		for i, r := range rs {
			ratings[i] = r.Rating
		}
		sort.Float64s(ratings)
		ar.med = ratings[len(ratings)/2]
		_ = name
	}
	// one index per arena, keyed by normalised name
	index := map[string]map[string]Row{}
	for name, ar := range arenas {
		by := map[string]Row{}
		for _, r := range ar.by {
			for _, k := range Keys(r.Name) {
				if old, ok := by[k]; !ok || r.Votes > old.Votes {
					by[k] = r
				}
			}
		}
		index[name] = by
	}
	var out []Score
	for id, m := range models {
		for name, ar := range arenas {
			if !arenaServes(name, m.Endpoint) {
				continue
			}
			by := index[name]
			for _, k := range catalogKeys(id) {
				r, ok := by[k]
				if !ok {
					continue
				}
				sc := Score{Model: id, Arena: name, Rating: r.Rating, Lo: r.Lo, Hi: r.Hi, Matched: r.Name, Date: r.Date, Votes: r.Votes}
				if ar.raw {
					sc.Value = round(r.Value)
				} else {
					// score the conservative end of the 95% interval: a model with few votes has a wide interval and
					// must prove itself before it is trusted as much as a well measured one
					floor := r.Lo
					if floor <= 0 || floor > r.Rating {
						floor = r.Rating
					}
					sc.Value = round(winProb(floor, ar.med))
				}
				out = append(out, sc)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Arena < out[j].Arena
	})
	return out
}

// arenaServes reports whether an arena's scores apply to a catalog endpoint.
func arenaServes(arena, endpoint string) bool {
	switch arena {
	case "swebench", "aider":
		return endpoint == "chat"
	case "asr":
		return endpoint == "audio_transcription"
	}
	for _, a := range Arenas {
		if a.Config == arena {
			return a.Endpoint == endpoint
		}
	}
	return false
}

// winProb is the Bradley-Terry / Elo probability that a model rated r beats a model rated ref in a vote.
func winProb(r, ref float64) float64 { return 1 / (1 + math.Pow(10, (ref-r)/400)) }

func round(v float64) float64 { return math.Round(v*1000) / 1000 }
