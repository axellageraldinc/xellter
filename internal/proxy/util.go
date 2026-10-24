package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// tail keeps the last n bytes written to it.
type tail struct {
	buf []byte
	n   int
}

func newTail(n int) *tail { return &tail{n: n} }

func (t *tail) write(p []byte) {
	if len(p) >= t.n {
		t.buf = append(t.buf[:0], p[len(p)-t.n:]...)
		return
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.n:]...)
	}
}

func (t *tail) bytes() []byte { return t.buf }

type usage struct{ in, out, cached int }

// findUsage scans the head and tail of a response (JSON or SSE) for every
// "usage" object and keeps the largest value of each token counter. This
// covers OpenAI chat/responses and Anthropic shapes without parsing whole bodies.
func findUsage(chunks ...[]byte) usage {
	var u usage
	key := []byte(`"usage"`)
	for _, c := range chunks {
		rest := c
		for {
			i := bytes.Index(rest, key)
			if i < 0 {
				break
			}
			rest = rest[i+len(key):]
			j := bytes.IndexByte(rest, ':')
			if j < 0 || len(bytes.TrimSpace(rest[:j])) != 0 {
				continue
			}
			dec := json.NewDecoder(bytes.NewReader(rest[j+1:]))
			var m map[string]any
			if dec.Decode(&m) != nil {
				continue
			}
			u.in = max(u.in, num(m["prompt_tokens"]), num(m["input_tokens"]))
			u.out = max(u.out, num(m["completion_tokens"]), num(m["output_tokens"]))
			u.cached = max(u.cached, num(m["cache_read_input_tokens"]))
			for _, d := range []string{"prompt_tokens_details", "input_tokens_details"} {
				if sub, ok := m[d].(map[string]any); ok {
					u.cached = max(u.cached, num(sub["cached_tokens"]))
				}
			}
		}
	}
	return u
}

func num(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func boundaryOf(ctype string) string {
	_, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		return ""
	}
	return params["boundary"]
}

// multipartModel returns the "model" form field of a multipart body.
func multipartModel(raw []byte, boundary string) string {
	if boundary == "" {
		return ""
	}
	mr := multipart.NewReader(bytes.NewReader(raw), boundary)
	for {
		p, err := mr.NextPart()
		if err != nil {
			return ""
		}
		if p.FormName() == "model" {
			b, _ := io.ReadAll(io.LimitReader(p, 256))
			return strings.TrimSpace(string(b))
		}
	}
}

// rewriteMultipart copies a multipart body replacing the "model" field.
func rewriteMultipart(raw []byte, boundary, model string) ([]byte, error) {
	var out bytes.Buffer
	w := multipart.NewWriter(&out)
	if err := w.SetBoundary(boundary); err != nil {
		return nil, err
	}
	mr := multipart.NewReader(bytes.NewReader(raw), boundary)
	wrote := false
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if p.FormName() == "model" {
			if err := w.WriteField("model", model); err != nil {
				return nil, err
			}
			wrote = true
			continue
		}
		pw, err := w.CreatePart(p.Header)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(pw, p); err != nil {
			return nil, err
		}
	}
	if !wrote {
		if err := w.WriteField("model", model); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
