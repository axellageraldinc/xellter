package admin

import (
	"math"
	"testing"

	"github.com/axellageraldinc/xellter/internal/store"
)

func TestSpendOfCountsRTKCachedAndPercent(t *testing.T) {
	m := []savingModel{{ID: "big", Found: true, InIDR: 40000, OutIDR: 200000, Cached: 15000}, {ID: "gone"}}
	// 1000 in (200 cached), 100 out, RTK removed 400 chars = 100 tokens, actual cost Rp 5.
	got := spendOf(m, 1000, 100, 200, 400, 5, nil)
	wantRTK := 100 * 40000.0 / 1e6
	wantBase := (800*40000.0+200*15000.0+100*200000.0)/1e6 + wantRTK
	if math.Abs(got[0].Baseline-wantBase) > 1e-9 || math.Abs(got[0].RTK-wantRTK) > 1e-9 {
		t.Fatalf("baseline/rtk = %v / %v, want %v / %v", got[0].Baseline, got[0].RTK, wantBase, wantRTK)
	}
	if math.Abs(got[0].Saved-(wantBase-5)) > 1e-9 || math.Abs(got[0].Percent-(wantBase-5)/wantBase*100) > 1e-9 {
		t.Fatalf("saved/pct = %v / %v", got[0].Saved, got[0].Percent)
	}
	if got[1].Baseline != 0 || got[1].Saved != 0 {
		t.Fatalf("an unknown model must price nothing: %+v", got[1])
	}
	sum := sumSavings(m, [][]saving{got, got})
	if math.Abs(sum[0].Percent-got[0].Percent) > 1e-9 {
		t.Fatalf("two equal rows must keep the percentage: %v vs %v", sum[0].Percent, got[0].Percent)
	}
}

func TestCompareList(t *testing.T) {
	d := []string{"a", "b"}
	if g := compareList("", d); len(g) != 2 {
		t.Fatal(g)
	}
	if g := compareList(" x, y ,x,", d); len(g) != 2 || g[0] != "x" || g[1] != "y" {
		t.Fatal(g)
	}
}

func TestMediaIsComparedOnItsOwnKind(t *testing.T) {
	img := savingModel{ID: "img", Found: true, unit: map[string]float64{"image": 1000}}
	txt := savingModel{ID: "txt", Found: true, InIDR: 1000, OutIDR: 1000, Cached: 1000}
	// 10 images charged Rp 4 in all, plus a chat of 1000 in / 0 out charged Rp 1; the row cost is 5.
	media := []store.UnitSpend{{Unit: "image", Units: 10, Cost: 4}, {Unit: "service", Units: 3, Cost: 0.5}}
	got := spendOf([]savingModel{img, txt}, 1000, 0, 0, 0, 5.5, media)
	if math.Abs(got[0].Baseline-10000) > 1e-9 || math.Abs(got[0].Saved-(10000-4)) > 1e-9 {
		t.Fatalf("image model: %+v", got[0])
	}
	// the text model prices the chat only (1000 tokens x Rp 1000 per 1M = Rp 1) against the chat cost 1
	if math.Abs(got[1].Baseline-1) > 1e-9 || math.Abs(got[1].covered-1) > 1e-9 {
		t.Fatalf("text model: %+v", got[1])
	}
}

func TestSystemOneIsNotPricedAsText(t *testing.T) {
	txt := savingModel{ID: "txt", Found: true, InIDR: 1000, OutIDR: 1000, Cached: 1000}
	// 3000 tokens in all, 2000 of them System One's; the row cost 10, of which System One 8
	media := []store.UnitSpend{{Unit: "systemone", Cost: 8, InTok: 2000}}
	got := spendOf([]savingModel{txt}, 3000, 0, 0, 0, 10, media)
	if math.Abs(got[0].Baseline-1) > 1e-9 || math.Abs(got[0].covered-2) > 1e-9 {
		t.Fatalf("only the 1000 text tokens and their Rp 2 count: %+v", got[0])
	}
	// a System One model prices the System One tokens only, against System One's own cost
	sys := savingModel{ID: "sys", Found: true, service: true, InIDR: 500, OutIDR: 500, Cached: 500}
	got = spendOf([]savingModel{sys}, 3000, 0, 0, 0, 10, media)
	if math.Abs(got[0].Baseline-1) > 1e-9 || math.Abs(got[0].covered-8) > 1e-9 {
		t.Fatalf("2000 tokens x Rp 500 per 1M = Rp 1 against the Rp 8 charged: %+v", got[0])
	}
}
