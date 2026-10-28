package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinimalYAMLWorksAndDefaultsFill(t *testing.T) {
	c, err := Parse([]byte("clients:\n  - name: babu\n    enabled: true\n    keys:\n      - {name: main, key_env: KENARI_KEY_BABU_MAIN}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Listen != "127.0.0.1:8480" || c.Kenari.AccountKeyEnv != "KENARI_ACCOUNT_KEY" {
		t.Fatalf("defaults missing: %+v", c.Clients[0].Keys[0])
	}
	// the routing constants keep their defaults, and a client with no purpose is general
	if c.Routing.TopModels != 10 || c.Routing.EvictErrors != 3 {
		t.Fatalf("routing defaults missing: %+v", c.Routing)
	}
	if c.Clients[0].Purpose != "general" {
		t.Fatalf("purpose default missing: %q", c.Clients[0].Purpose)
	}
}

// A preset may carry any name the owner likes, but the old fixed names keep
// their meaning, so a config written before presets could be renamed still
// loads. A client holds at most one preset per category.
func TestPresetNamesCoverEveryCategory(t *testing.T) {
	want := map[string]string{
		"chat": "text", "images": "image", "videos": "video", "audio_speech": "tts",
		"audio_transcription": "stt", "music": "music", "systemone": "systemone",
	}
	if len(Categories) != len(want) {
		t.Fatalf("categories: %v", Categories)
	}
	for kind, name := range want {
		if PresetName(kind) != name {
			t.Errorf("%s -> %q, want %q", kind, PresetName(kind), name)
		}
		if got := CategoryOfPreset(name); got != kind {
			t.Errorf("%s -> %q, want %q", name, got, kind)
		}
		p := DefaultPreset(kind)
		if p.Category != kind || p.Optimization != "balanced" || len(p.Models) != 0 {
			t.Errorf("default preset %s: %+v", kind, p)
		}
	}
	if CategoryOfPreset("budget") != "" || PresetName("telepathy") != "" {
		t.Fatal("unknown categories and presets must not map to anything")
	}

	// A preset of the owner's own name loads, and so does one of the old names
	// without a category.
	own, err := Parse([]byte("clients:\n  - name: x\n    presets:\n      my-fast-text: {category: chat, optimization: cheapest}\n"))
	if err != nil {
		t.Fatalf("a preset of the owner's own name was refused: %v", err)
	}
	if _, ok := own.Clients[0].Presets["my-fast-text"]; !ok {
		t.Fatal("the preset was not kept")
	}
	old, err := Parse([]byte("clients:\n  - name: x\n    presets:\n      text: {optimization: balanced}\n"))
	if err != nil {
		t.Fatalf("a preset of an old name without a category was refused: %v", err)
	}
	if got := old.Clients[0].Presets["text"].Category; got != "chat" {
		t.Fatalf("the old name must still mean chat, got %q", got)
	}
}

func TestValidationCatchesMistakes(t *testing.T) {
	cases := map[string]string{
		"bad secret name":          "clients:\n  - name: x\n    keys:\n      - {name: main, key_env: sk-live-123}\n",
		"duplicate key in client":  "clients:\n  - name: x\n    keys:\n      - {name: a, key_env: K1}\n      - {name: a, key_env: K2}\n",
		"secret shared by clients": "clients:\n  - name: x\n    keys:\n      - {name: a, key_env: K1}\n  - name: y\n    keys:\n      - {name: a, key_env: K1}\n",
		"duplicate client":         "clients:\n  - {name: x}\n  - {name: x}\n",
		"bad purpose":              "clients:\n  - {name: x, purpose: telepathy}\n",
		"bad preset name":          "clients:\n  - name: x\n    presets:\n      'a b': {category: chat, optimization: balanced}\n",
		"preset of no category":    "clients:\n  - name: x\n    presets:\n      budget: {optimization: balanced}\n",
		"preset of the wrong kind": "clients:\n  - name: x\n    presets:\n      text: {category: images, optimization: balanced}\n",
		"two presets of one kind":  "clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: balanced}\n      mine: {category: chat, optimization: balanced}\n",
		"bad optimization":         "clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: cheapestest}\n",
		"model listed twice":       "clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: balanced, models: [a, a]}\n",
		"empty model id":           "clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: balanced, models: ['  ']}\n",
		"bad account key env":      "kenari: {account_key_env: not-an-env}\n",
		"top_models out of range":  "routing: {top_models: 51}\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a second preset of the same category cannot exist: the names are the categories
	if _, err := Parse([]byte("clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: balanced}\n")); err != nil {
		t.Fatalf("a valid preset was refused: %v", err)
	}
}

func TestSaveKeepsHistoryAndNeverStoresSecrets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "xellter.yaml")
	c := Default()
	c.Server.DataDir = filepath.Join(dir, "data")
	c.Clients = []Client{{
		Name: "babu", Enabled: true, Purpose: "coding",
		Keys:    []Upstream{{Name: "main", KeyEnv: "KENARI_KEY_BABU_MAIN"}},
		Presets: map[string]Preset{"text": {Category: "chat", Optimization: "balanced", Models: []string{"cheap-b", "pro-c"}}},
	}}
	hist := &memHistory{}
	for i := range 3 {
		c.Routing.TopModels = 10 + i
		if err := Save(file, c, hist); err != nil {
			t.Fatal(err)
		}
	}
	if h := hist.ListHistory(); len(h) != 2 {
		t.Fatalf("history %v", h)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", st.Mode())
	}
	if err := SetSecret(filepath.Join(dir, "s.env"), "KENARI_KEY_BABU_MAIN", "super-secret"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if strings.Contains(string(b), "super-secret") {
		t.Fatal("secret leaked into config")
	}
	if LoadSecrets(filepath.Join(dir, "s.env"))["KENARI_KEY_BABU_MAIN"] != "super-secret" {
		t.Fatal("secret not stored")
	}
	if err := DeleteSecret(filepath.Join(dir, "s.env"), "KENARI_KEY_BABU_MAIN"); err != nil || len(LoadSecrets(filepath.Join(dir, "s.env"))) != 0 {
		t.Fatal("secret not deleted")
	}
	if _, err := ReadHistory(hist, "../../etc/passwd"); err == nil {
		t.Fatal("history id path traversal")
	}
	back, err := Load(file)
	if err != nil || len(back.Clients[0].Presets) != 1 || back.Clients[0].Presets["text"].Optimization != "balanced" {
		t.Fatalf("round trip lost the preset: %v %+v", err, back)
	}
	if len(back.Clients[0].Presets["text"].Models) != 2 || back.Clients[0].Purpose != "coding" {
		t.Fatalf("round trip lost the pool or the purpose: %+v", back.Clients[0])
	}
}

func TestMatchAny(t *testing.T) {
	if !MatchAny([]string{"*"}, "x") || !MatchAny([]string{"deepseek-*"}, "deepseek-v4") || MatchAny([]string{"deepseek-*"}, "glm-5") || MatchAny(nil, "x") {
		t.Fatal("glob matching broken")
	}
}

// A config built outside Parse (the panel decodes JSON) is normalised the same way: missing routing knobs and
// a missing optimization are filled, not left as zero values.
func TestNormalizeFillsWhatThePanelLeavesOut(t *testing.T) {
	c := &Config{Clients: []Client{{
		Name: "x", Enabled: true, Purpose: "",
		Presets: map[string]Preset{"text": {Models: []string{"a"}}},
	}}}
	if err := Normalize(c); err != nil {
		t.Fatal(err)
	}
	if c.Routing.TopModels != 10 || c.Routing.EvictCooldownMin != 720 {
		t.Fatalf("routing defaults missing: %+v", c.Routing)
	}
	p := c.Clients[0].Presets["text"]
	if c.Clients[0].Purpose != "general" || p.Category != "chat" || p.Optimization != "balanced" {
		t.Fatalf("preset defaults missing: %+v", c.Clients[0])
	}
	// a negative refresh means never, and must stay 0 instead of becoming a default
	c2 := &Config{Routing: Routing{RankRefreshMinutes: -5}}
	if err := Normalize(c2); err != nil {
		t.Fatal(err)
	}
	if c2.Routing.RankRefreshMinutes != 0 {
		t.Fatalf("a negative refresh must mean never: %d", c2.Routing.RankRefreshMinutes)
	}
}

// The rewrite deleted a pile of knobs. Unknown keys are otherwise ignored, so each retired section and each
// retired preset key must be refused by name instead of looking like it still works (ADR 0017).
func TestRetiredKeysAreRefused(t *testing.T) {
	cases := map[string]string{
		"rules section":         "rules:\n  sticky_minutes: 5\n",
		"resilience section":    "resilience:\n  cooldown_base_s: 30\n",
		"rtk section":           "rtk:\n  enabled: false\n",
		"benchmarks map":        "benchmarks:\n  gpt-6: 80\n",
		"preset weights":        "clients:\n  - name: x\n    presets:\n      text: {weights: {cost: 1}}\n",
		"preset require":        "clients:\n  - name: x\n    presets:\n      text: {require: {tool_call: true}}\n",
		"preset min_quality":    "clients:\n  - name: x\n    presets:\n      text: {min_quality: 0.5}\n",
		"preset max_candidates": "clients:\n  - name: x\n    presets:\n      text: {max_candidates: 5}\n",
		"preset strategy":       "clients:\n  - name: x\n    presets:\n      text: {strategy: priority}\n",
		"preset rr_band":        "clients:\n  - name: x\n    presets:\n      text: {rr_band: 0.1}\n",
		"preset mode":           "clients:\n  - name: x\n    presets:\n      text: {mode: combo}\n",
		"preset cost_profile":   "clients:\n  - name: x\n    presets:\n      text: {cost_profile: cheap}\n",
		"preset description":    "clients:\n  - name: x\n    presets:\n      text: {description: cheap}\n",
	}
	for name, y := range cases {
		_, err := Parse([]byte(y))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "retired by the rewrite") {
			t.Errorf("%s: error does not name the reason: %v", name, err)
		}
	}
}

// The global sticky knob is gone: stickiness is a per-preset toggle now, so a
// file that still carries the old key is refused instead of being ignored.
func TestTheGlobalStickyKnobIsRefused(t *testing.T) {
	_, err := Parse([]byte("routing:\n  sticky_session: true\n"))
	if err == nil {
		t.Fatal("routing.sticky_session was accepted")
	}
	if !strings.Contains(err.Error(), "sticky_session") {
		t.Fatalf("the error must name the knob: %v", err)
	}
}

// A preset that says nothing about stickiness sticks, as it always did. A
// preset that says `sticky: false` roams.
func TestPresetStickyDefaultsToOn(t *testing.T) {
	c, err := Parse([]byte("clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: balanced}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Clients[0].Presets["text"].IsSticky() {
		t.Fatal("a preset that says nothing must stick")
	}
	roam, err := Parse([]byte("clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: balanced, sticky: false}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if roam.Clients[0].Presets["text"].IsSticky() {
		t.Fatal("sticky: false must not stick")
	}
	stay, err := Parse([]byte("clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: balanced, sticky: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !stay.Clients[0].Presets["text"].IsSticky() {
		t.Fatal("sticky: true must stick")
	}
}

// The panel reads the config as JSON and writes it back, so the toggle has to
// survive that round trip: a preset that does not stick must not come back
// sticky.
func TestPresetStickySurvivesTheJSONRoundTrip(t *testing.T) {
	c, err := Parse([]byte("clients:\n  - name: x\n    presets:\n      text: {category: chat, optimization: balanced, sticky: false}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	out := &Config{}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	if out.Clients[0].Presets["text"].IsSticky() {
		t.Fatal("sticky: false must not come back sticky")
	}
	if out.Clients[0].Presets["text"].Sticky == nil {
		t.Fatal("the flag must survive the round trip")
	}
}

// The old global switch becomes the per-preset toggle, so a file that turned
// stickiness off does not come back sticking after the migration.
func TestMigrateCarriesTheOldStickySwitchOver(t *testing.T) {
	off, _, err := Migrate([]byte("routing:\n  sticky_session: false\nclients:\n  - name: x\n    presets:\n      text: {modality: chat, models: [a]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if off.Clients[0].Presets["text"].IsSticky() {
		t.Fatal("sticky_session: false must migrate to a preset that does not stick")
	}
	on, _, err := Migrate([]byte("routing:\n  sticky_session: true\nclients:\n  - name: x\n    presets:\n      text: {modality: chat, models: [a]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !on.Clients[0].Presets["text"].IsSticky() {
		t.Fatal("sticky_session: true must migrate to a preset that sticks")
	}
	silent, _, err := Migrate([]byte("clients:\n  - name: x\n    presets:\n      text: {modality: chat, models: [a]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !silent.Clients[0].Presets["text"].IsSticky() {
		t.Fatal("a file that never said anything must keep the sticky default")
	}
}

type memHistory struct{ m map[string][]byte }

func (h *memHistory) AddHistory(id string, y []byte) {
	if h.m == nil {
		h.m = map[string][]byte{}
	}
	h.m[id] = y
}
func (h *memHistory) HasHistory(id string) bool { _, ok := h.m[id]; return ok }
func (h *memHistory) GetHistory(id string) ([]byte, error) {
	return h.m[id], nil
}
func (h *memHistory) ListHistory() []string {
	var out []string
	for id := range h.m {
		out = append(out, id)
	}
	return out
}
