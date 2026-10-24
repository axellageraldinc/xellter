package rank

import (
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
)

// RefCost estimates the price (IDR) of one reference request of the shadow's
// cost profile at list prices. The bool is false when the price is unknown.
func RefCost(m *catalog.Model, p config.CostProfile, now time.Time, lookahead time.Duration) (float64, bool) {
	if m.Free {
		return 0, true
	}
	switch m.Endpoint {
	case "chat":
		in, out, cached := m.Effective(now, lookahead)
		if in == 0 && out == 0 {
			return 0, false
		}
		inTok, outTok := float64(p.InputTokens), float64(p.OutputTokens)
		if inTok == 0 && outTok == 0 {
			inTok, outTok = 4000, 600
		}
		return (inTok*(1-p.CachedRatio)*in + inTok*p.CachedRatio*cached + outTok*out) / 1e6, true
	case "images":
		if l := pickLine(m, "image", ""); l != nil {
			return l.IDR * maxf(p.Images, 1), true
		}
	case "videos":
		if l := pickLine(m, "second", p.Variant); l != nil {
			return l.IDR * maxf(p.Seconds, 5), true
		}
	case "audio_speech":
		if l := pickLine(m, "1k_chars", ""); l != nil {
			return l.IDR * maxf(p.Chars, 1000) / 1000, true
		}
	case "audio_transcription":
		if l := pickLine(m, "second", ""); l != nil {
			return l.IDR * maxf(p.Seconds, 60), true
		}
	case "music":
		if l := pickLine(m, "song", ""); l != nil {
			return l.IDR, true
		}
	}
	return 0, false
}

// pickLine returns the price line of the unit, preferring the variant, else the cheapest.
func pickLine(m *catalog.Model, unit, variant string) *catalog.Line {
	var best *catalog.Line
	for i := range m.Lines {
		l := &m.Lines[i]
		if l.Unit != unit {
			continue
		}
		if variant != "" && l.Variant == variant {
			return l
		}
		if best == nil || l.IDR < best.IDR {
			best = l
		}
	}
	return best
}

// UsageCost is the catalog-price cost of a finished chat request.
func UsageCost(m *catalog.Model, inTok, outTok, cachedTok int, freeCache bool, now time.Time) float64 {
	if m.Free {
		return 0
	}
	in, out, cached := m.Effective(now, 0)
	if cachedTok > inTok {
		cachedTok = inTok
	}
	cp := cached
	if freeCache {
		cp = 0
	}
	return (float64(inTok-cachedTok)*in + float64(cachedTok)*cp + float64(outTok)*out) / 1e6
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
