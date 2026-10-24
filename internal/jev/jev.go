// Package jev scores candidate chat models with kenari's System One ("JEV")
// decision model: each candidate answers a few golden prompts, JEV judges the
// answers, and the average becomes the model's quality score (0..1). Calls are
// capped by a daily Rupiah budget and run in the background.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/rank"
	"github.com/axellageraldinc/xellter/internal/store"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// Judge runs evaluations.
type Judge struct {
	Cfg      func() *config.Config
	Cat      *catalog.Catalog
	Pool     *upstream.Pool
	St       *store.Store
	HC       *http.Client
	Rankings func() map[string]*rank.Ranking
	Logf     func(string, ...any)
	Now      func() time.Time

	mu      sync.Mutex
	day     string
	spent   float64
	running bool
	last    RunInfo
	down    time.Time // the judge model answered 5xx: do not try before this time
}

// errJudgeDown means kenari cannot serve the judge model right now; the round stops and retries later.
var errJudgeDown = errors.New("the judge model is unavailable at kenari right now")

// RunInfo describes the latest evaluation round for the panel.
type RunInfo struct {
	At     time.Time `json:"at"`
	Judged int       `json:"judged"`
	Error  string    `json:"error"`
}

// Last returns the latest evaluation round.
func (j *Judge) Last() RunInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.last
}

func (j *Judge) note(judged int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.last = RunInfo{At: j.Now(), Judged: judged}
	if err != nil {
		j.last.Error = err.Error()
	}
}

// Budget returns spent and limit for today.
func (j *Judge) Budget() (spent, limit float64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rollover()
	return j.spent, j.Cfg().JEV.DailyBudgetIDR
}

func (j *Judge) rollover() {
	d := j.Now().Format("2006-01-02")
	if j.day != d {
		j.day, j.spent = d, 0
	}
}

func (j *Judge) charge(v float64) {
	j.mu.Lock()
	j.rollover()
	j.spent += v
	j.mu.Unlock()
}

func (j *Judge) overBudget() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rollover()
	return j.spent >= j.Cfg().JEV.DailyBudgetIDR
}

// Run evaluates due models every interval until ctx ends.
func (j *Judge) Run(ctx context.Context) {
	wait := 45 * time.Second // the first round soon after start, then every 10 minutes
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 10 * time.Minute
		if j.Cfg().JEV.Enabled {
			if n, err := j.RunDue(ctx, 4); err != nil {
				j.Logf("jev run: %v", err)
			} else if n > 0 {
				j.Logf("jev: judged %d model(s)", n)
			}
		}
	}
}

// RunDue evaluates up to max models that have no recent score; trial models go first.
func (j *Judge) RunDue(ctx context.Context, max int) (int, error) {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return 0, errors.New("already running")
	}
	j.running = true
	j.mu.Unlock()
	defer func() { j.mu.Lock(); j.running = false; j.mu.Unlock() }()

	cfg := j.Cfg()
	scores, _ := j.St.JEVScores()
	every := time.Duration(cfg.JEV.EveryHours) * time.Hour
	var todo []string
	seen := map[string]bool{}
	rks := j.Rankings()
	names := make([]string, 0, len(rks))
	for n := range rks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, pass := range []string{"trial", "main"} {
		for _, n := range names {
			rk := rks[n]
			if rk.Modality != "chat" {
				continue
			}
			list := rk.Candidates
			if pass == "trial" {
				list = rk.Trial
			}
			for _, c := range list {
				if s, ok := scores[c.Model]; ok && j.Now().Sub(s.TS) < every {
					continue
				}
				if !seen[c.Model] {
					seen[c.Model] = true
					todo = append(todo, c.Model)
				}
			}
		}
	}
	done := 0
	var lastErr error
	for _, m := range todo {
		if done >= max || ctx.Err() != nil {
			break
		}
		if j.overBudget() {
			lastErr = errors.New("daily JEV budget used")
			break
		}
		if _, err := j.Evaluate(ctx, m); err != nil {
			j.Logf("jev evaluate %s: %v", m, err)
			lastErr = err
			if errors.Is(err, errJudgeDown) || errors.Is(err, errNoSystemKey) {
				break // every other model would fail the same way; try again next round
			}
			lastErr = fmt.Errorf("%s: %w", m, err)
			continue
		}
		done++
	}
	if len(todo) > 0 || lastErr != nil {
		j.note(done, lastErr)
	}
	if done == 0 && lastErr != nil && len(todo) > 0 {
		return 0, lastErr
	}
	return done, nil
}

// Evaluate scores one model and stores the result.
func (j *Judge) Evaluate(ctx context.Context, model string) (float64, error) {
	cfg := j.Cfg()
	snap := j.Cat.Snapshot()
	m := snap.Models[model]
	if m == nil || m.Endpoint != "chat" {
		return 0, errors.New("not a chat model")
	}
	j.mu.Lock()
	down := j.down
	j.mu.Unlock()
	if j.Now().Before(down) {
		return 0, fmt.Errorf("%w; next try at %s", errJudgeDown, down.Format("15:04"))
	}
	key, err := j.systemKey(snap, m)
	if err != nil {
		return 0, err
	}
	chain := j.judgeChain(cfg, snap)
	if len(chain) == 0 {
		return 0, fmt.Errorf("judge model %q not in catalog", cfg.JEV.Model)
	}
	var sum float64
	n := 0
	for _, prompt := range cfg.JEV.Prompts {
		if j.overBudget() {
			break
		}
		answer, in, out, err := j.chat(ctx, cfg, key, model, prompt)
		j.log(model, key.Name, err, in, out)
		if err != nil {
			return 0, err
		}
		j.charge(rank.UsageCost(m, in, out, 0, false, j.Now()))
		score, used, jin, err := j.judgeWithFallback(ctx, cfg, key, chain, prompt, answer)
		if err != nil {
			if errors.Is(err, errJudgeDown) {
				j.mu.Lock()
				j.down = j.Now().Add(10 * time.Minute)
				j.mu.Unlock()
			}
			return 0, err
		}
		j.charge(float64(jin) * used.InIDR / 1e6)
		sum += score
		n++
	}
	if n == 0 {
		return 0, errors.New("no prompts evaluated")
	}
	avg := sum / float64(n)
	j.St.SetJEV(model, avg, n)
	return avg, nil
}

func (j *Judge) log(model, key string, err error, in, out int) {
	r := store.Request{TS: j.Now().UnixMilli(), Client: "_system", Model: model, Upstream: key, Path: "jev", Status: 200, OK: err == nil, InTok: in, OutTok: out, Billing: "payg"}
	if err != nil {
		r.Status, r.Err = 0, err.Error()
	}
	j.St.Log(r)
}

// errNoSystemKey is returned when no account key is configured.
var errNoSystemKey = errors.New("judging needs the dashboard's account key: add it on the Overview page (Kenari plan & balance)")

// systemKey returns the dashboard's own key, reserved for one request of model m.
func (j *Judge) systemKey(snap *catalog.Snapshot, m *catalog.Model) (*upstream.Key, error) {
	k := j.Pool.SystemKey()
	if k == nil {
		return nil, errNoSystemKey
	}
	if why := k.ReserveSystem(m, snap, j.Now()); why != upstream.OK {
		return nil, fmt.Errorf("the account key cannot take a request now: %s", why)
	}
	return k, nil
}

func (j *Judge) post(ctx context.Context, cfg *config.Config, key *upstream.Key, path string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Kenari.BaseURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	req.Header.Set("User-Agent", cfg.Kenari.UserAgent)
	resp, err := j.HC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		key.Failure("", resp.StatusCode, raw, parseRA(resp), cfg.Resilience, j.Now())
		return fmt.Errorf("%s: HTTP %d: %.200s", path, resp.StatusCode, raw)
	}
	return json.Unmarshal(raw, out)
}

func parseRA(r *http.Response) time.Duration {
	var n int
	fmt.Sscanf(r.Header.Get("Retry-After"), "%d", &n)
	return time.Duration(n) * time.Second
}

func (j *Judge) chat(ctx context.Context, cfg *config.Config, key *upstream.Key, model, prompt string) (string, int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			In  int `json:"prompt_tokens"`
			Out int `json:"completion_tokens"`
		} `json:"usage"`
	}
	err := j.post(ctx, cfg, key, "/v1/chat/completions", map[string]any{
		"model": model, "max_tokens": 600, "messages": []map[string]string{{"role": "user", "content": prompt}},
	}, &out)
	if err != nil {
		return "", 0, 0, err
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return "", out.Usage.In, out.Usage.Out, errors.New("empty answer")
	}
	return out.Choices[0].Message.Content, out.Usage.In, out.Usage.Out, nil
}

func (j *Judge) judge(ctx context.Context, cfg *config.Config, key *upstream.Key, model, prompt, answer string) (float64, int, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var out struct {
		Answers map[string]struct {
			Noul float64 `json:"noul"`
		} `json:"answers"`
		Usage struct {
			In int `json:"input_tokens"`
		} `json:"usage"`
	}
	err := j.post(ctx, cfg, key, "/v1/systemone", map[string]any{
		"model": model,
		"state": "TASK:\n" + prompt + "\n\nANSWER:\n" + answer,
		"questions": map[string]any{"q": map[string]any{
			"type":         "noul",
			"instructions": "Does the ANSWER correctly, completely and helpfully do what the TASK asks, written in the language of the TASK, without refusing, rambling or making things up?",
			"criteria": map[string]string{
				"true":  "The answer is correct, complete, on-topic and in the right language.",
				"false": "The answer is wrong, incomplete, off-topic, in the wrong language or a refusal.",
			},
		}},
	}, &out)
	if err != nil {
		return 0, 0, err
	}
	a, ok := out.Answers["q"]
	if !ok {
		return 0, out.Usage.In, errors.New("no answer from judge")
	}
	return a.Noul, out.Usage.In, nil
}

// judgeChain is the configured judge followed by the other free System One models (jev first, then clef), the same
// idea as aixell's classifier chain: when one judge is down at kenari the next one answers.
func (j *Judge) judgeChain(cfg *config.Config, snap *catalog.Snapshot) []*catalog.Model {
	var chain []*catalog.Model
	if m := snap.Models[cfg.JEV.Model]; m != nil && m.Endpoint == "systemone" {
		chain = append(chain, m)
	}
	var rest []*catalog.Model
	for _, id := range snap.IDs() {
		if m := snap.Models[id]; m.Endpoint == "systemone" && m.Free && id != cfg.JEV.Model {
			rest = append(rest, m)
		}
	}
	sort.SliceStable(rest, func(a, b int) bool {
		ja, jb := strings.HasPrefix(rest[a].ID, "jev"), strings.HasPrefix(rest[b].ID, "jev")
		if ja != jb {
			return ja
		}
		return rest[a].ID < rest[b].ID
	})
	return append(chain, rest...)
}

// judgeWithFallback asks the judges in order and moves on when one is unavailable (5xx) or too slow.
func (j *Judge) judgeWithFallback(ctx context.Context, cfg *config.Config, key *upstream.Key, chain []*catalog.Model, prompt, answer string) (float64, *catalog.Model, int, error) {
	var last error
	for _, m := range chain {
		score, in, err := j.judge(ctx, cfg, key, m.ID, prompt, answer)
		if err == nil {
			return score, m, in, nil
		}
		last = err
		if !strings.Contains(err.Error(), "HTTP 5") && !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
			return 0, nil, 0, err // a real answer error (400, no answer): another judge would not help
		}
	}
	return 0, nil, 0, fmt.Errorf("%w (%d judges tried): %v", errJudgeDown, len(chain), last)
}
