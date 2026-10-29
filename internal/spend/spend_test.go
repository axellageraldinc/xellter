package spend

import "testing"

func TestFind(t *testing.T) {
	hour := map[string]float64{"loop": 200000, "busy": 60000, "quiet": 400, "new": 90000}
	// busy usually spends 40000/h: a 1.5x hour is ordinary. loop usually spends 1000/h. quiet is under the floor.
	week := map[string]float64{
		"loop": 200000 + 1000*float64(Window-1), "busy": 60000 + 40000*float64(Window-1), "quiet": 400,
		"new": 90000,
	}
	got := Find(hour, week, 4, 50000)
	if len(got) != 2 || got[0].Client != "loop" || got[1].Client != "new" {
		t.Fatalf("spikes: %+v", got)
	}
	if got[0].UsualIDR < 999 || got[0].UsualIDR > 1001 {
		t.Fatalf("usual: %v", got[0].UsualIDR)
	}
	if got[1].UsualIDR != 0 {
		t.Fatalf("a client with no history has a zero baseline: %v", got[1].UsualIDR)
	}
	if Find(hour, week, 0, 50000) != nil {
		t.Fatal("a factor of 0 switches the alert off")
	}
}
