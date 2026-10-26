package rtk

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDiffKeepsChangesDropsContext(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/a.go b/a.go\nindex 123..456 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,500 +1,500 @@\n")
	for i := range 500 {
		fmt.Fprintf(&b, " context line %d that is not interesting\n", i)
		if i == 250 {
			b.WriteString("-removed line\n+added line\n")
		}
	}
	out, f := Compress(b.String())
	if f != "git_diff" || len(out) > len(b.String())/10 {
		t.Fatalf("filter=%q size %d -> %d", f, b.Len(), len(out))
	}
	for _, want := range []string{"diff --git a/a.go", "@@ -1,500", "-removed line", "+added line", "unchanged lines"} {
		if !strings.Contains(out, want) {
			t.Fatalf("lost %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "index 123") {
		t.Fatal("index line should be dropped")
	}
}

func TestGrepCapsMatchesPerFile(t *testing.T) {
	var b strings.Builder
	for i := range 300 {
		fmt.Fprintf(&b, "src/big.go:%d:    foo := bar(%d)\n", i, i)
	}
	b.WriteString("src/small.go:7:    needle\n")
	out, f := Compress(b.String())
	if f != "grep" || !strings.Contains(out, "src/small.go:7") || !strings.Contains(out, "more matches omitted") {
		t.Fatalf("filter=%q\n%s", f, out)
	}
}

func TestLogsCollapseRepeatsKeepErrors(t *testing.T) {
	var b strings.Builder
	for i := range 600 {
		fmt.Fprintf(&b, "2026-10-24T05:%02d:00Z INFO heartbeat ok id=%d\n", i%60, i)
		if i == 300 {
			b.WriteString("2026-10-24T05:05:00Z ERROR database connection refused\n")
		}
	}
	out, f := Compress(b.String())
	if f != "logs" || !strings.Contains(out, "database connection refused") || !strings.Contains(out, "(x") || len(out) > len(b.String())/5 {
		t.Fatalf("filter=%q len %d -> %d\n%.600s", f, b.Len(), len(out), out)
	}
}

func TestLsLongFormat(t *testing.T) {
	var b strings.Builder
	b.WriteString("total 4000\n")
	for i := range 200 {
		fmt.Fprintf(&b, "-rw-r--r-- 1 user user %d Oct 24 05:00 file-%03d.txt\n", i*10, i)
	}
	out, f := Compress(b.String())
	if f != "ls" || !strings.Contains(out, "file-000.txt 0") || !strings.Contains(out, "more entries") {
		t.Fatalf("filter=%q\n%.300s", f, out)
	}
}

// RTK has no knobs (ADR 0017): the thresholds are constants, so a small text passes through and a huge one
// is truncated to maxChars without anyone configuring it.
func TestSmallAndUnknownTextIsUntouchedOrTruncated(t *testing.T) {
	if out, f := Compress("short"); out != "short" || f != "" {
		t.Fatal("short text must pass through")
	}
	big := strings.Repeat("lorem ipsum dolor sit amet ", 2000)
	out, f := Compress(big)
	if f != "truncate" || len(out) > maxChars+100 || !strings.Contains(out, "removed by xellter RTK") {
		t.Fatalf("filter=%q len=%d", f, len(out))
	}
}

func TestProcessOnlyTouchesToolResults(t *testing.T) {
	big := strings.Repeat("src/x.go:1:    same line\nsrc/x.go:2:    other line\n", 400)
	user := strings.Repeat("user text user text ", 500)
	mk := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	body := map[string]json.RawMessage{"messages": mk([]map[string]any{
		{"role": "user", "content": user},
		{"role": "tool", "content": big},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "1", "content": big}, {"type": "text", "text": user}}},
	})}
	res := Process(body)
	if res.Items != 2 || res.Saved <= 0 {
		t.Fatalf("result %+v", res)
	}
	var msgs []map[string]json.RawMessage
	_ = json.Unmarshal(body["messages"], &msgs)
	var u string
	_ = json.Unmarshal(msgs[0]["content"], &u)
	if u != user {
		t.Fatal("user text was modified")
	}
	if !strings.Contains(string(msgs[2]["content"]), "user text user text") {
		t.Fatal("anthropic text block was modified")
	}
}

// The Responses protocol carries tool output in a different shape; it is compressed too.
func TestProcessHandlesResponsesToolOutput(t *testing.T) {
	big := strings.Repeat("line of tool output that is repeated\n", 400)
	mk := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	body := map[string]json.RawMessage{"input": mk([]map[string]any{
		{"type": "message", "content": "hi"},
		{"type": "function_call_output", "call_id": "1", "output": big},
	})}
	if res := Process(body); res.Items != 1 || res.Saved <= 0 {
		t.Fatalf("responses tool output not compressed: %+v", res)
	}
}

func TestFailOpenOnGarbage(t *testing.T) {
	body := map[string]json.RawMessage{"messages": json.RawMessage(`{"not":"an array"}`), "input": json.RawMessage(`"str"`)}
	if res := Process(body); res.Items != 0 {
		t.Fatal("garbage must be ignored")
	}
}
