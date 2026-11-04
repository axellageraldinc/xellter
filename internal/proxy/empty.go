package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// An answer with nothing in it (ADR 0046). Some free models answer HTTP 200 with an empty message; the caller gets
// nothing to use and the gateway used to count it as a success.

// errEmpty is the reason recorded for such an answer, in the request log and in the model's eviction record.
const errEmpty = "empty answer"

// maxInspected is the largest non-streamed body that is read in full to look for an empty answer. A body that big
// is not empty, and is passed on untouched.
const maxInspected = 8 << 20

// emptyCompletion reports whether a non-streamed chat completion says nothing: it has choices, and not one of
// them has text, a tool call, a function call or audio. A body that is not a chat completion is never empty.
func emptyCompletion(b []byte) bool {
	var v struct {
		Choices []struct {
			Message struct {
				Content      json.RawMessage `json:"content"`
				ToolCalls    json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage `json:"function_call"`
				Audio        json.RawMessage `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(b, &v) != nil || len(v.Choices) == 0 {
		return false
	}
	for _, c := range v.Choices {
		m := c.Message
		if hasText(m.Content) || present(m.ToolCalls) || present(m.FunctionCall) || present(m.Audio) {
			return false
		}
	}
	return true
}

// present is true for a JSON value that holds something: not missing, null, "", [] or {}.
func present(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", `""`, "[]", "{}":
		return false
	}
	return true
}

// hasText is true for message content with something in it: a string that is not blank, or a list of parts of
// which one has text or is not a text part (an image, for example).
func hasText(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		for _, p := range parts {
			if strings.TrimSpace(p.Text) != "" || (p.Type != "" && p.Type != "text") {
				return true
			}
		}
		return false
	}
	return present(raw)
}

// emptyStream reports whether a whole SSE chat stream carried no text and no tool call. It is given the bytes of
// the stream and only judges one it can see in full: at least one parsable chunk, none with content.
func emptyStream(b []byte) bool {
	seen := false
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[5:])
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		var v struct {
			Choices []struct {
				Delta struct {
					Content      json.RawMessage `json:"content"`
					ToolCalls    json.RawMessage `json:"tool_calls"`
					FunctionCall json.RawMessage `json:"function_call"`
					Audio        json.RawMessage `json:"audio"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &v) != nil {
			return false // a stream this code does not understand is left alone
		}
		seen = true
		for _, c := range v.Choices {
			d := c.Delta
			if hasText(d.Content) || present(d.ToolCalls) || present(d.FunctionCall) || present(d.Audio) {
				return false
			}
		}
	}
	return seen
}

// inspect reads a non-streamed answer in full, up to maxInspected, and puts it back so it can still be relayed.
// It reports whether the answer is empty.
func inspect(res sendResult) (sendResult, bool) {
	body := res.resp.Body
	b, err := io.ReadAll(io.LimitReader(body, maxInspected+1))
	if err == nil && len(b) <= maxInspected {
		err = io.EOF // the whole body is in b
	}
	res.resp.Body = &replayBody{first: b, err: err, rest: body}
	return res, err == io.EOF && emptyCompletion(b)
}
