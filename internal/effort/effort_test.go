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

// ---- persistence ----

// A restart must not cost the client the knowledge the gateway had already paid for.
func TestTheRegistrySurvivesARestart(t *testing.T) {
	now := time.Now()
	r := NewRegistry()
	// A model seen thinking, then quiet: it honours the effort.
	r.Observe("honest", None, true, now)
	r.Observe("honest", None, false, now)
	// A model that answers 400 to the field.
	r.Record("stubborn", None, Rejects, now)

	saved := r.State()

	fresh := NewRegistry()
	kept, dropped := fresh.Restore(saved, now, DefaultTTL)
	if kept != 2 {
		t.Fatalf("restored %d facts, wanted 2", kept)
	}
	if dropped != 0 {
		t.Fatalf("dropped %d facts, wanted none", dropped)
	}
	if v := fresh.Verdict("honest", None); v != Honours {
		t.Fatalf("the restored verdict for the honest model is %v, wanted honours", v)
	}
	if v := fresh.Verdict("stubborn", None); v != Rejects {
		t.Fatalf("the restored verdict for the stubborn model is %v, wanted rejects", v)
	}
	// The thinkers travel with the facts: without that record, the next quiet answer
	// from the honest model could not be credited as obedience.
	if !fresh.Thinks("honest") {
		t.Fatal("the restored registry does not know the honest model thinks")
	}
}

// A fact is a measurement, and a provider can change a model's behaviour. A fact older
// than the trust window is dropped so the model is measured again.
func TestAnOldFactIsDropped(t *testing.T) {
	now := time.Now()
	r := NewRegistry()
	r.Record("m", None, Honours, now)

	saved := r.State()
	fresh := NewRegistry()
	kept, dropped := fresh.Restore(saved, now.Add(2*DefaultTTL), DefaultTTL)
	if kept != 0 {
		t.Fatalf("kept %d old facts, wanted none", kept)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d facts, wanted 1", dropped)
	}
	if v := fresh.Verdict("m", None); v != Unknown {
		t.Fatalf("an expired fact is still trusted: %v", v)
	}
}

// A refusal is a property of the API's parameter validation, not a passing mood. It
// holds however old it is: sending the field again would fail the call.
func TestARefusalIsKeptHoweverOldItIs(t *testing.T) {
	now := time.Now()
	r := NewRegistry()
	r.Record("m", None, Rejects, now)

	saved := r.State()
	fresh := NewRegistry()
	kept, dropped := fresh.Restore(saved, now.Add(100*DefaultTTL), DefaultTTL)
	if kept != 1 {
		t.Fatalf("kept %d facts, wanted the refusal", kept)
	}
	if dropped != 0 {
		t.Fatalf("dropped %d facts, wanted none", dropped)
	}
	if v := fresh.Verdict("m", None); v != Rejects {
		t.Fatalf("the refusal was dropped: %v", v)
	}
}

// A refusal is never replaced by a weaker fact, whatever order the saved file is in.
func TestARestoredRefusalIsNotWeakened(t *testing.T) {
	now := time.Now()
	r := NewRegistry()
	r.Record("m", None, Rejects, now)
	r.Record("n", None, Honours, now)

	// The file lists the weak fact last, and Restore reads it last.
	saved := r.State()
	fresh := NewRegistry()
	fresh.Restore(saved, now, DefaultTTL)
	if v := fresh.Verdict("m", None); v != Rejects {
		t.Fatalf("the refusal was weakened: %v", v)
	}
}

// VerdictAt forgets a fact that is too old to trust, so the routing stops preferring a
// model whose behaviour may have changed.
func TestVerdictAtForgetsAnOldFact(t *testing.T) {
	now := time.Now()
	r := NewRegistry()
	r.Record("m", None, Honours, now)
	if v := r.VerdictAt("m", None, now, time.Hour); v != Honours {
		t.Fatalf("a fresh fact is not trusted: %v", v)
	}
	if v := r.VerdictAt("m", None, now.Add(2*time.Hour), time.Hour); v != Unknown {
		t.Fatalf("an old fact is still trusted: %v", v)
	}
	// A refusal is exempt.
	r.Record("s", None, Rejects, now)
	if v := r.VerdictAt("s", None, now.Add(100*time.Hour), time.Hour); v != Rejects {
		t.Fatalf("a refusal expires: %v", v)
	}
}

// The version tells a saver whether there is anything new to write.
func TestVersionCountsChanges(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	if r.Version() != 0 {
		t.Fatal("a new registry has changes already")
	}
	r.Record("m", None, Honours, now)
	if r.Version() == 0 {
		t.Fatal("a recorded fact did not count as a change")
	}
	// Recording the same fact again is still a write, but the version only has to
	// answer "is there anything new", so a change is counted.
	r.Record("m", None, Honours, now)
	if r.Version() == 0 {
		t.Fatal("the version stayed at zero")
	}
}
