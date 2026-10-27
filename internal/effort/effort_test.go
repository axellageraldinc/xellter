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

func TestObserveCreditsObedienceOnlyToAModelKnownToThink(t *testing.T) {
	r := NewRegistry()
	now := time.Now()

	// Never seen thinking: unknown, and therefore neither preferred nor excluded.
	if v := r.Observe("quiet", None, false, now); v != Unknown {
		t.Fatalf("a model that never thought must stay unknown, got %v", v)
	}
	if r.Verdict("quiet", None) != Unknown {
		t.Fatal("the unknown verdict was not kept")
	}

	// Seen thinking, then quiet: it obeyed.
	r.Observe("thinker", None, true, now)
	if v := r.Observe("thinker", None, false, now); v != Honours {
		t.Fatalf("a model that stopped thinking honoured the effort, got %v", v)
	}

	// Seen thinking, then thinking again: it ignored the effort.
	if v := r.Observe("thinker", None, true, now); v != Ignores {
		t.Fatalf("a model that kept thinking ignored the effort, got %v", v)
	}
}

func TestARefusalIsPermanent(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	r.Record("m", None, Rejects, now)
	if v := r.Verdict("m", None); v != Rejects {
		t.Fatalf("the refusal was not recorded, got %v", v)
	}
	// A later success does not undo a refusal: the model refuses the field.
	if v := r.Observe("m", None, false, now.Add(time.Hour)); v != Rejects {
		t.Fatalf("a refusal was undone by a later success, got %v", v)
	}
}

func TestVerdictsArePerLevel(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	r.Record("m", None, Rejects, now)
	if v := r.Verdict("m", Low); v != Unknown {
		t.Fatalf("a refusal of one level must not speak for another, got %v", v)
	}
}

func TestKnows(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	if r.Knows("m", None) {
		t.Fatal("an unmeasured model is not known")
	}
	r.Record("m", None, Honours, now)
	if !r.Knows("m", None) {
		t.Fatal("a measured model is known")
	}
}

func TestStale(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	r.Record("m", None, Honours, now)
	if r.Stale("m", None, now.Add(time.Minute), time.Hour) {
		t.Fatal("a fresh observation is not stale")
	}
	if !r.Stale("m", None, now.Add(2*time.Hour), time.Hour) {
		t.Fatal("an old observation is stale")
	}
}

func TestAll(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	r.Record("m", None, Honours, now)
	r.Record("n", Low, Ignores, now)
	all := r.All()
	if len(all) != 2 {
		t.Fatalf("the snapshot has %d entries, wanted 2", len(all))
	}
}
