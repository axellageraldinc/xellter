package proxy

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEstimateEnglishIsAboutAQuarterOfTheCharacters(t *testing.T) {
	text := strings.Repeat("the quick brown fox jumps over the lazy dog ", 100) // 4400 chars
	got := estimateTokens([]byte(`{"model":"m","messages":[{"role":"user","content":"` + text + `"}]}`))
	if got < 1000 || got > 1250 {
		t.Fatalf("4400 characters of English estimated at %d tokens, want about 1100", got)
	}
}

// A megabyte of base64 is one image, not 250 000 tokens.
func TestEstimateAnInlineImageIsNotItsSize(t *testing.T) {
	img := base64.StdEncoding.EncodeToString(make([]byte, 1<<20))
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + img + `"}}]}]}`
	if got := estimateTokens([]byte(body)); got > 1500 {
		t.Fatalf("an image estimated at %d tokens", got)
	}
	// Anthropic's shape: the data is a bare base64 string.
	body = `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + img + `"}}]}]}`
	if got := estimateTokens([]byte(body)); got > 1500 {
		t.Fatalf("a bare base64 image estimated at %d tokens", got)
	}
}

// One CJK character is about a token; the old rule said a quarter.
func TestEstimateCJKIsOneTokenPerCharacter(t *testing.T) {
	text := strings.Repeat("你好世界", 250) // 1000 characters
	got := estimateTokens([]byte(`{"messages":[{"role":"user","content":"` + text + `"}]}`))
	if got < 950 || got > 1200 {
		t.Fatalf("1000 CJK characters estimated at %d tokens, want about 1000", got)
	}
}

func TestEstimateKeysAreNotCountedAsTextValues(t *testing.T) {
	// A key and a value of the same text: the key is one token, the value is estimated as text.
	long := strings.Repeat("abcdefgh", 100)
	withKey := estimateTokens([]byte(`{"` + long + `":1}`))
	if withKey > 10 {
		t.Fatalf("a long key estimated at %d tokens", withKey)
	}
}

func TestEstimateSurvivesBadJSON(t *testing.T) {
	for _, b := range []string{``, `{`, `not json`, `[1,2`, `{"a":`} {
		_ = estimateTokens([]byte(b)) // must not panic
	}
}

func TestTheScaleLearnsFromWhatTheUpstreamReports(t *testing.T) {
	var s tokenScale
	if got := s.scaled("c", 1000); got != 1000 {
		t.Fatalf("without data the estimate stands, got %d", got)
	}
	for i := 0; i < 40; i++ {
		s.learn("c", 1000, 2000) // this client's requests are twice what the estimate says
	}
	if got := s.scaled("c", 1000); got < 1900 || got > 2100 {
		t.Fatalf("after learning the estimate is %d, want about 2000", got)
	}
	if got := s.scaled("other", 1000); got != 1000 {
		t.Fatal("a client's ratio leaked to another client")
	}
	s.learn("c", 100, 50000) // too small to learn from
	s.learn("c", 5000, 0)    // no usage reported
	if got := s.scaled("c", 1000); got < 1900 || got > 2100 {
		t.Fatalf("noise moved the ratio: %d", got)
	}
}

func TestTheScaleIsBounded(t *testing.T) {
	var s tokenScale
	for i := 0; i < 50; i++ {
		s.learn("c", 1000, 1_000_000)
	}
	if got := s.scaled("c", 1000); got > 4100 {
		t.Fatalf("one absurd report must not scale the estimate to %d", got)
	}
}
