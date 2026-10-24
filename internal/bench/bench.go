// Package bench downloads public quality leaderboards and maps them onto kenari model ids.
//
// Source: the Arena (LMArena) leaderboard dataset on Hugging Face, which publishes human-preference
// ratings (Bradley-Terry, with confidence intervals) for text, text-to-image and text-to-video models and is
// refreshed regularly. It needs no key. A rating is turned into the probability that the model beats a typical
// (median) model of its arena in a head-to-head vote, a 0..1 number that is the "benchmark" component of ranking.
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

const dataset = "lmarena-ai/leaderboard-dataset"

// Arenas maps an Arena config to the catalog endpoint it ranks.
var Arenas = []struct{ Config, Endpoint string }{
	{"text", "chat"},
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
}

// Score is a leaderboard entry mapped onto one catalog model.
type Score struct {
	Model   string  `json:"model"`
	Value   float64 `json:"value"`  // 0..1, win probability against the arena's median model
	Rating  float64 `json:"rating"` // published rating
	Lo      float64 `json:"lo"`     // 95% interval
	Hi      float64 `json:"hi"`
	Arena   string  `json:"arena"`
	Matched string  `json:"matched"` // the leaderboard name that was matched
	Date    string  `json:"date"`
	Votes   float64 `json:"votes"`
}

func get(ctx context.Context, hc *http.Client, u string, out any) error {
	var last error
	for i := 0; i < 4; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "xellter")
		resp, err := hc.Do(req)
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return json.Unmarshal(b, out)
			}
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

// Fetch downloads the "overall" leaderboards. The dataset lists each arena's overall category first, so paging stops
// at the first row of another category.
func Fetch(ctx context.Context, hc *http.Client, base string) ([]Row, error) {
	var all []Row
	var firstErr error
	for _, a := range Arenas {
		rows, err := fetchArena(ctx, hc, base, a.Config)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", a.Config, err)
			}
			continue
		}
		all = append(all, rows...)
	}
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

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
// release number ("muse-wonder-1-0" is listed as "Muse Wonder").
func catalogKeys(id string) []string {
	ts := tokens(id)
	if len(ts) == 0 {
		return nil
	}
	keys := []string{strings.Join(ts, "-")}
	if n := len(ts); n > 2 && ts[n-1] == "0" && isDigits(ts[n-2]) {
		keys = append(keys, strings.Join(ts[:n-2], "-"))
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
	}
	arenas := map[string]*arena{}
	for _, a := range Arenas {
		var rs []Row
		for _, r := range rows {
			if r.Arena == a.Config {
				rs = append(rs, r)
			}
		}
		if len(rs) == 0 {
			continue
		}
		ratings := make([]float64, len(rs))
		for i, r := range rs {
			ratings[i] = r.Rating
		}
		sort.Float64s(ratings)
		ar := &arena{by: map[string]Row{}, med: ratings[len(ratings)/2]}
		for _, r := range rs {
			for _, k := range Keys(r.Name) {
				if old, ok := ar.by[k]; !ok || r.Votes > old.Votes {
					ar.by[k] = r
				}
			}
		}
		arenas[a.Config] = ar
	}
	var out []Score
	for id, m := range models {
		for _, a := range Arenas {
			if a.Endpoint != m.Endpoint || arenas[a.Config] == nil {
				continue
			}
			ar := arenas[a.Config]
			for _, k := range catalogKeys(id) {
				if r, ok := ar.by[k]; ok {
					// score the conservative end of the 95% interval: a model with few votes has a wide interval and
					// must prove itself before it is trusted as much as a well measured one
					floor := r.Lo
					if floor <= 0 || floor > r.Rating {
						floor = r.Rating
					}
					out = append(out, Score{Model: id, Value: round(winProb(floor, ar.med)), Rating: r.Rating, Lo: r.Lo, Hi: r.Hi, Arena: a.Config, Matched: r.Name, Date: r.Date, Votes: r.Votes})
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// winProb is the Bradley-Terry / Elo probability that a model rated r beats a model rated ref in a vote.
func winProb(r, ref float64) float64 { return 1 / (1 + math.Pow(10, (ref-r)/400)) }

func round(v float64) float64 { return math.Round(v*1000) / 1000 }
