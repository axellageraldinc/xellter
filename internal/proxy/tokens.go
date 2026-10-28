package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strings"
	"sync"
)

// Token estimation (ADR 0031).
//
// The gateway needs to know roughly how big a request is: to put the models that can hold it first, and to give
// a long prompt more time before its first token. It has no tokenizer (every model has its own), so it estimates
// from the text and then corrects itself with what the upstream reports.
//
// The estimate reads the JSON, not the bytes. Four bytes per token is a good rule for English prose, but a
// request body is mostly something else: base64 images (a megabyte of characters that cost about a thousand
// tokens), JSON punctuation and key names, and text in languages where one character is one token or more.

// mediaTokens is what an inline image or file costs a model, whatever its size in base64.
const mediaTokens = 1100

// estimateTokens estimates the prompt tokens of a JSON request body.
func estimateTokens(raw []byte) int {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var total float64
	// inObject[i] says container i is an object; wantKey[i] says its next string is a key.
	var inObject, wantKey []bool
	for {
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			break
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				inObject = append(inObject, true)
				wantKey = append(wantKey, true)
				total += 1.5 // the braces and a separator
			case '[':
				inObject = append(inObject, false)
				wantKey = append(wantKey, false)
				total += 1
			default: // '}' or ']'
				inObject, wantKey = inObject[:len(inObject)-1], wantKey[:len(wantKey)-1]
				if n := len(wantKey); n > 0 && inObject[n-1] {
					wantKey[n-1] = true
				}
			}
			continue
		case string:
			if n := len(wantKey); n > 0 && inObject[n-1] && wantKey[n-1] {
				wantKey[n-1] = false
				total += 1 // a key name is one token or close to it
				continue
			}
			total += tokensOfString(v)
		case json.Number:
			total += 1
		case bool, nil:
			total += 1
		}
		if n := len(wantKey); n > 0 && inObject[n-1] {
			wantKey[n-1] = true
		}
	}
	return int(math.Ceil(total))
}

// tokensOfString estimates one string value.
func tokensOfString(s string) float64 {
	if len(s) > 2048 && looksLikeBase64(s) || strings.HasPrefix(s, "data:") && strings.Contains(s[:min(len(s), 80)], ";base64,") {
		return mediaTokens
	}
	var ascii, wide float64
	for _, r := range s {
		switch {
		case r < 0x80:
			ascii++
		case r < 0x250: // Latin with accents: two bytes, about half a token
			wide += 0.5
		case r >= 0x2E80 && r <= 0x9FFF, r >= 0xAC00 && r <= 0xD7AF, r >= 0xF900 && r <= 0xFAFF: // CJK, kana, hangul
			wide++
		case r >= 0x1F000: // emoji
			wide += 2
		default: // Cyrillic, Greek, Arabic, Indic, Thai ...
			wide += 0.6
		}
	}
	return ascii/4 + wide
}

// looksLikeBase64 reports whether the start of a long string is base64 (no spaces, only the base64 alphabet).
func looksLikeBase64(s string) bool {
	for i := 0; i < 256 && i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/', c == '=', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// tokenScale learns, per client, how far the estimate is from what the upstream reports. A client whose
// requests are mostly code, or mostly Indonesian, or mostly tool output, has its own ratio.
type tokenScale struct {
	mu sync.Mutex
	by map[string]float64
}

const (
	scaleMin   = 0.25
	scaleMax   = 4.0
	scaleSmall = 400 // below this many estimated tokens the ratio is noise
)

// scaled applies what was learned about the client to an estimate.
func (t *tokenScale) scaled(client string, est int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.by[client]; ok {
		return int(float64(est)*r + 0.5)
	}
	return est
}

// learn folds one observation (the estimate for a request, and the prompt tokens the upstream reported) into
// the client's ratio. A moving average, so one odd request moves it a little.
func (t *tokenScale) learn(client string, est, actual int) {
	if est < scaleSmall || actual <= 0 {
		return
	}
	r := math.Min(scaleMax, math.Max(scaleMin, float64(actual)/float64(est)))
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.by == nil {
		t.by = map[string]float64{}
	}
	if old, ok := t.by[client]; ok {
		r = 0.8*old + 0.2*r
	}
	t.by[client] = r
}

// estimate is the calibrated prompt size of a request, in tokens. 0 when it cannot say (not JSON).
func (s *Server) estimate(cl string, b *reqBody) int {
	if b == nil || b.multipart || len(b.raw) == 0 {
		return 0
	}
	if b.est == 0 {
		b.est = estimateTokens(b.raw)
	}
	return s.scale.scaled(cl, b.est)
}
