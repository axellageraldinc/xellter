// Package rtk compresses large tool outputs (git diff, grep, ls, logs, generic
// text) inside chat requests before they are sent upstream. It only touches
// tool results, never user or assistant text, and fails open: on any problem
// the original text is kept.
package rtk

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/axellageraldinc/xellter/internal/config"
)

// Filter is one compression strategy. Filters are tried in order; the first
// whose Match returns true handles the text.
type Filter interface {
	Name() string
	Enabled(c config.RTK) bool
	Match(text string) bool
	Apply(text string, c config.RTK) string
}

// Result reports what a Process call changed.
type Result struct {
	Items int // tool outputs compressed
	Saved int // characters removed
}

// Filters is the ordered built-in set. Additional filters can be appended by
// code that embeds the package.
var Filters = []Filter{diffFilter{}, grepFilter{}, lsFilter{}, logFilter{}, truncFilter{}}

// Compress runs the first matching enabled filter over text.
func Compress(text string, c config.RTK) (out string, filter string) {
	if len(text) < c.MinChars {
		return text, ""
	}
	defer func() {
		if recover() != nil {
			out, filter = text, ""
		}
	}()
	for _, f := range Filters {
		if !f.Enabled(c) || !f.Match(text) {
			continue
		}
		res := f.Apply(text, c)
		if len(res) < len(text) {
			if c.Truncate && c.MaxChars > 0 && len(res) > c.MaxChars {
				res = headTail(res, c.MaxChars)
			}
			return res, f.Name()
		}
	}
	return text, ""
}

// Process rewrites tool outputs in a decoded request body in place. It
// understands OpenAI chat (role=tool), Anthropic (tool_result blocks) and
// OpenAI Responses (function_call_output) shapes.
func Process(body map[string]json.RawMessage, c config.RTK) (res Result) {
	defer func() { _ = recover() }()
	if raw, ok := body["messages"]; ok {
		var msgs []map[string]json.RawMessage
		if json.Unmarshal(raw, &msgs) == nil {
			changed := false
			for _, m := range msgs {
				var role string
				_ = json.Unmarshal(m["role"], &role)
				content := m["content"]
				if role == "tool" || role == "function" {
					if nc, r := fixContent(content, c); r.Items > 0 {
						m["content"], changed = nc, true
						res.Items, res.Saved = res.Items+r.Items, res.Saved+r.Saved
					}
					continue
				}
				if len(content) > 0 && content[0] == '[' { // anthropic blocks
					var blocks []map[string]json.RawMessage
					if json.Unmarshal(content, &blocks) != nil {
						continue
					}
					bc := false
					for _, b := range blocks {
						var typ string
						_ = json.Unmarshal(b["type"], &typ)
						if typ != "tool_result" {
							continue
						}
						if nc, r := fixContent(b["content"], c); r.Items > 0 {
							b["content"], bc = nc, true
							res.Items, res.Saved = res.Items+r.Items, res.Saved+r.Saved
						}
					}
					if bc {
						if nb, err := json.Marshal(blocks); err == nil {
							m["content"], changed = nb, true
						}
					}
				}
			}
			if changed {
				if nb, err := json.Marshal(msgs); err == nil {
					body["messages"] = nb
				}
			}
		}
	}
	if raw, ok := body["input"]; ok && len(raw) > 0 && raw[0] == '[' { // OpenAI Responses
		var items []map[string]json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			changed := false
			for _, it := range items {
				var typ string
				_ = json.Unmarshal(it["type"], &typ)
				if typ != "function_call_output" {
					continue
				}
				if nc, r := fixContent(it["output"], c); r.Items > 0 {
					it["output"], changed = nc, true
					res.Items, res.Saved = res.Items+r.Items, res.Saved+r.Saved
				}
			}
			if changed {
				if nb, err := json.Marshal(items); err == nil {
					body["input"] = nb
				}
			}
		}
	}
	return res
}

// fixContent compresses a JSON string, or the text parts of a content array.
func fixContent(raw json.RawMessage, c config.RTK) (json.RawMessage, Result) {
	var r Result
	if len(raw) == 0 {
		return raw, r
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return raw, r
		}
		out, f := Compress(s, c)
		if f == "" {
			return raw, r
		}
		nb, _ := json.Marshal(out)
		return nb, Result{1, len(s) - len(out)}
	}
	if raw[0] == '[' {
		var parts []map[string]json.RawMessage
		if json.Unmarshal(raw, &parts) != nil {
			return raw, r
		}
		for _, p := range parts {
			var t string
			if json.Unmarshal(p["text"], &t) != nil {
				continue
			}
			if out, f := Compress(t, c); f != "" {
				p["text"], _ = json.Marshal(out)
				r.Items++
				r.Saved += len(t) - len(out)
			}
		}
		if r.Items > 0 {
			nb, _ := json.Marshal(parts)
			return nb, r
		}
	}
	return raw, r
}

func headTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head, tail := max*6/10, max*3/10
	cut := len(s) - head - tail
	return s[:cutRune(s, head)] + fmt.Sprintf("\n… [%d chars removed by xellter RTK] …\n", cut) + s[len(s)-tail:]
}

func cutRune(s string, i int) int {
	for i > 0 && i < len(s) && s[i]&0xC0 == 0x80 {
		i--
	}
	return i
}

func clip(line string, n int) string {
	if len(line) <= n {
		return line
	}
	return line[:cutRune(line, n)] + "…"
}

// ---- git diff ----

type diffFilter struct{}

func (diffFilter) Name() string              { return "git_diff" }
func (diffFilter) Enabled(c config.RTK) bool { return c.GitDiff }
func (diffFilter) Match(t string) bool {
	return strings.HasPrefix(t, "diff --git") || (strings.Contains(t, "\n@@ ") && strings.Contains(t, "\n+++ ") && strings.Contains(t, "\n--- "))
}
func (diffFilter) Apply(t string, c config.RTK) string {
	lines := strings.Split(t, "\n")
	keep := make([]bool, len(lines))
	ctx := c.DiffContext
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff --git"), strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "@@"), strings.HasPrefix(l, "rename "), strings.HasPrefix(l, "new file"), strings.HasPrefix(l, "deleted file"), strings.HasPrefix(l, "Binary files"):
			keep[i] = true
		case strings.HasPrefix(l, "+"), strings.HasPrefix(l, "-"):
			for j := i - ctx; j <= i+ctx; j++ {
				if j >= 0 && j < len(lines) {
					keep[j] = true
				}
			}
		}
	}
	var out []string
	skipped := 0
	flush := func() {
		if skipped > 0 {
			out = append(out, fmt.Sprintf("  … %d unchanged lines", skipped))
			skipped = 0
		}
	}
	for i, l := range lines {
		if strings.HasPrefix(l, "index ") {
			continue
		}
		if keep[i] {
			flush()
			out = append(out, clip(l, 400))
		} else {
			skipped++
		}
	}
	flush()
	return strings.Join(out, "\n")
}

// ---- grep ----

var (
	grepRe = regexp.MustCompile(`^[\w./\\-]+:\d+(:\d+)?[:-]`)
	tsRe   = regexp.MustCompile(`^(\d{4}-\d\d-\d\d[T ]\d\d:\d\d|\[?\d\d:\d\d:\d\d)`) // log timestamps look like file:line
)

type grepFilter struct{}

func (grepFilter) Name() string              { return "grep" }
func (grepFilter) Enabled(c config.RTK) bool { return c.Grep }
func (grepFilter) Match(t string) bool {
	return share(t, func(l string) bool { return grepRe.MatchString(l) && !tsRe.MatchString(l) }) >= 0.6
}
func (grepFilter) Apply(t string, c config.RTK) string {
	lines := strings.Split(t, "\n")
	perFile := map[string]int{}
	var out []string
	dropped := 0
	var prev string
	for _, l := range lines {
		if l == prev { // consecutive duplicates
			dropped++
			continue
		}
		prev = l
		file := l
		if i := strings.Index(l, ":"); i > 0 {
			file = l[:i]
		}
		perFile[file]++
		if perFile[file] > 12 {
			dropped++
			continue
		}
		out = append(out, clip(l, 300))
	}
	if dropped > 0 {
		out = append(out, fmt.Sprintf("… %d more matches omitted (max 12 per file)", dropped))
	}
	return strings.Join(out, "\n")
}

// ---- ls ----

var lsLongRe = regexp.MustCompile(`^[dlcbps-][rwxsStT-]{9}[.+@]?\s+\d+\s+\S+\s+\S+\s+(\d+)\s+\S+\s+\d+\s+\S+\s+(.+)$`)

type lsFilter struct{}

func (lsFilter) Name() string              { return "ls" }
func (lsFilter) Enabled(c config.RTK) bool { return c.LS }
func (lsFilter) Match(t string) bool {
	return share(t, func(l string) bool { return lsLongRe.MatchString(l) || strings.HasPrefix(l, "total ") }) >= 0.7
}
func (lsFilter) Apply(t string, c config.RTK) string {
	var out []string
	for _, l := range strings.Split(t, "\n") {
		if m := lsLongRe.FindStringSubmatch(l); m != nil {
			prefix := ""
			if l[0] == 'd' {
				prefix = "d "
			}
			out = append(out, prefix+m[2]+" "+m[1])
		}
	}
	if len(out) > 150 {
		out = append(out[:120], fmt.Sprintf("… +%d more entries", len(out)-120))
	}
	return strings.Join(out, "\n")
}

// ---- logs ----

var (
	logRe   = regexp.MustCompile(`(?i)(\b(trace|debug|info|warn(ing)?|error|fatal|panic)\b|^\d{4}-\d\d-\d\d[T ]\d\d:\d\d|^\[\d)`)
	digitRe = regexp.MustCompile(`\d+`)
	hotRe   = regexp.MustCompile(`(?i)(error|fail|fatal|panic|exception|traceback|warn)`)
)

type logFilter struct{}

func (logFilter) Name() string              { return "logs" }
func (logFilter) Enabled(c config.RTK) bool { return c.Logs }
func (logFilter) Match(t string) bool {
	return share(t, func(l string) bool { return logRe.MatchString(l) }) >= 0.6
}
func (logFilter) Apply(t string, c config.RTK) string {
	lines := strings.Split(t, "\n")
	var out []string
	var prevKey string
	repeat := 0
	flush := func() {
		if repeat > 0 {
			out[len(out)-1] += fmt.Sprintf("  (x%d)", repeat+1)
			repeat = 0
		}
	}
	for _, l := range lines {
		key := digitRe.ReplaceAllString(l, "#")
		if key == prevKey && len(out) > 0 {
			repeat++
			continue
		}
		flush()
		prevKey = key
		out = append(out, clip(l, 400))
	}
	flush()
	if len(out) > 300 { // keep the start, the end and every error/warning in between
		var mid []string
		for _, l := range out[100 : len(out)-100] {
			if hotRe.MatchString(l) {
				mid = append(mid, l)
			}
		}
		if len(mid) > 100 {
			mid = mid[:100]
		}
		res := append([]string{}, out[:100]...)
		res = append(res, fmt.Sprintf("… %d lines omitted, %d error/warn lines kept …", len(out)-200-len(mid), len(mid)))
		res = append(res, mid...)
		res = append(res, out[len(out)-100:]...)
		out = res
	}
	return strings.Join(out, "\n")
}

// ---- generic truncation ----

type truncFilter struct{}

func (truncFilter) Name() string              { return "truncate" }
func (truncFilter) Enabled(c config.RTK) bool { return c.Truncate }
func (truncFilter) Match(t string) bool       { return true }
func (truncFilter) Apply(t string, c config.RTK) string {
	if c.MaxChars <= 0 || len(t) <= c.MaxChars {
		return t
	}
	return headTail(t, c.MaxChars)
}

// share returns the fraction of non-empty lines for which f is true.
func share(t string, f func(string) bool) float64 {
	n, hit := 0, 0
	for _, l := range strings.Split(t, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n++
		if f(l) {
			hit++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(hit) / float64(n)
}
