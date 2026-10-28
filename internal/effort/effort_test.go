package effort

import (
	"testing"
	"time"
)

func TestParseReadsBothShapes(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want Level
		ok   bool
	}{
		{"the flat field", map[string]any{"reasoning_effort": "none"}, None, true},
		{"the nested field", map[string]any{"reasoning": map[string]any{"effort": "none"}}, None, true},
		{"the uppercase field", map[string]any{"reasoning_effort": "HIGH"}, High, true},
		{"a padded field", map[string]any{"reasoning_effort": "  medium "}, Medium, true},
		{"no effort", map[string]any{"model": "m"}, "", false},
		{"an empty effort", map[string]any{"reasoning_effort": ""}, "", false},
		{"an unknown effort", map[string]any{"reasoning_effort": "turbo"}, "", false},
		{"a nested effort that is not a string", map[string]any{"reasoning": map[string]any{"effort": 3}}, "", false},
		{"no body", nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Parse(c.body)
			if ok != c.ok || got != c.want {
				t.Fatalf("Parse gave (%q, %t), wanted (%q, %t)", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestARefusalIsPerModelAndPerLevel(t *testing.T) {
	r := NewRefusals()
	if r.Refuses("m", None) {
		t.Fatal("nothing is refused before it is seen")
	}
	r.Record("m", None, time.Now())
	if !r.Refuses("m", None) || r.Refuses("m", Low) || r.Refuses("other", None) {
		t.Fatal("a refusal belongs to one model and one level")
	}
}

func TestVersionChangesOnlyWhenARefusalIsNew(t *testing.T) {
	r := NewRefusals()
	v0 := r.Version()
	r.Record("m", None, time.Now())
	v1 := r.Version()
	r.Record("m", None, time.Now())
	if v1 == v0 || r.Version() != v1 {
		t.Fatalf("versions %d %d %d", v0, v1, r.Version())
	}
}

func TestRestoreKeepsOnlyUsableRefusals(t *testing.T) {
	r := NewRefusals()
	n := r.Restore([]Refusal{{Model: "a", Level: "none", At: 1}, {Model: "", Level: "none"}, {Model: "b", Level: "turbo"}, {Model: "a", Level: "none", At: 2}})
	if n != 1 || !r.Refuses("a", None) || r.Refuses("b", None) {
		t.Fatalf("kept %d", n)
	}
	if got := r.All(); len(got) != 1 || got[0].At != 1 {
		t.Fatalf("all = %+v", got)
	}
}
