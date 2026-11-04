package proxy

import "testing"

func TestEmptyCompletion(t *testing.T) {
	for _, c := range []struct {
		name, body string
		empty      bool
	}{
		{"empty string", `{"choices":[{"message":{"role":"assistant","content":""}}]}`, true},
		{"null content", `{"choices":[{"message":{"role":"assistant","content":null}}]}`, true},
		{"blank content", `{"choices":[{"message":{"content":" \n"}}]}`, true},
		{"reasoning only", `{"choices":[{"message":{"content":"","reasoning_content":"thinking"}}]}`, true},
		{"text", `{"choices":[{"message":{"content":"ok"}}]}`, false},
		{"tool call", `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`, false},
		{"empty tool list", `{"choices":[{"message":{"content":"","tool_calls":[]}}]}`, true},
		{"function call", `{"choices":[{"message":{"content":null,"function_call":{"name":"f"}}}]}`, false},
		{"parts with text", `{"choices":[{"message":{"content":[{"type":"text","text":"hi"}]}}]}`, false},
		{"parts without text", `{"choices":[{"message":{"content":[{"type":"text","text":""}]}}]}`, true},
		{"second choice has text", `{"choices":[{"message":{"content":""}},{"message":{"content":"x"}}]}`, false},
		{"no choices is not ours to judge", `{"choices":[]}`, false},
		{"an error body", `{"error":{"message":"x"}}`, false},
		{"not json", `hello`, false},
	} {
		if got := emptyCompletion([]byte(c.body)); got != c.empty {
			t.Errorf("%s: empty = %v, want %v", c.name, got, c.empty)
		}
	}
}

func TestEmptyStream(t *testing.T) {
	text := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	role := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n"
	fin := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	use := "data: {\"choices\":[],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n"
	tool := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"f\"}}]}}]}\n\n"
	for _, c := range []struct {
		name, body string
		empty      bool
	}{
		{"text", text, false},
		{"role and finish only", role + fin + use, true},
		{"tool call", role + tool + fin + use, false},
		{"nothing parsable is left alone", ": keep-alive\n\n", false},
		{"a chunk this code cannot read is left alone", "data: <html>\n\n", false},
	} {
		if got := emptyStream([]byte(c.body)); got != c.empty {
			t.Errorf("%s: empty = %v, want %v", c.name, got, c.empty)
		}
	}
}
