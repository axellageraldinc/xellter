package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsAreValidAndMinimalYAMLWorks(t *testing.T) {
	c, err := Parse([]byte("upstreams:\n  - {name: a, key_env: KENARI_KEY_A, enabled: true, tier: plan, plan: Indie}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Presets) < 8 || c.Presets["balanced"].Weights.Cost != 30 || c.Server.Listen != "127.0.0.1:8480" {
		t.Fatalf("defaults missing: %+v", c.Server)
	}
}

func TestValidationCatchesMistakes(t *testing.T) {
	cases := map[string]string{
		"unknown preset for client": "clients:\n  - {name: x, presets: [nope]}\n",
		"bad tier":                  "upstreams:\n  - {name: a, key_env: KENARI_KEY_A, tier: gold}\n",
		"bad key_env":               "upstreams:\n  - {name: a, key_env: sk-live-123, tier: free}\n",
		"duplicate upstream":        "upstreams:\n  - {name: a, key_env: K, tier: free}\n  - {name: a, key_env: K2, tier: free}\n",
		"combo without models":      "presets:\n  s: {modality: chat, mode: combo, strategy: priority, weights: {cost: 1}}\n",
		"bad modality":              "presets:\n  s: {modality: telepathy, mode: auto, strategy: priority, weights: {cost: 1}}\n",
		"negative weight":           "presets:\n  s: {modality: chat, mode: auto, strategy: priority, weights: {cost: -1}}\n",
		"canary over 100":           "rules: {canary_percent: 150}\n",
		"client unknown upstream":   "clients:\n  - {name: x, upstreams: [ghost]}\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSaveKeepsHistoryAndNeverStoresSecrets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "xellter.yaml")
	c := Default()
	c.Server.DataDir = filepath.Join(dir, "data")
	c.Presets = DefaultPresets()
	c.Upstreams = []Upstream{{Name: "a", KeyEnv: "KENARI_KEY_A", Enabled: true, Tier: "free"}}
	for i := 0; i < 3; i++ {
		c.Rules.CanaryPercent = float64(i)
		if err := Save(file, c); err != nil {
			t.Fatal(err)
		}
	}
	if h := History(c.Server.DataDir); len(h) != 2 {
		t.Fatalf("history %v", h)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", st.Mode())
	}
	if err := SetSecret(filepath.Join(dir, "s.env"), "KENARI_KEY_A", "super-secret"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if strings.Contains(string(b), "super-secret") {
		t.Fatal("secret leaked into config")
	}
	if LoadSecrets(filepath.Join(dir, "s.env"))["KENARI_KEY_A"] != "super-secret" {
		t.Fatal("secret not stored")
	}
	if _, err := ReadHistory(c.Server.DataDir, "../../etc/passwd"); err == nil {
		t.Fatal("history id path traversal")
	}
}

func TestMatchAny(t *testing.T) {
	if !MatchAny([]string{"*"}, "x") || !MatchAny([]string{"deepseek-*"}, "deepseek-v4") || MatchAny([]string{"deepseek-*"}, "glm-5") || MatchAny(nil, "x") {
		t.Fatal("glob matching broken")
	}
}

func TestOldShadowsKeysAreMigratedToPresets(t *testing.T) {
	c, err := Parse([]byte("shadows:\n  xl-old: {modality: chat, mode: auto, strategy: priority, weights: {cost: 1}}\nclients:\n  - {name: x, shadows: [xl-old]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Presets["xl-old"]; !ok || len(c.Clients[0].Presets) != 1 || c.OldShadows != nil {
		t.Fatalf("not migrated: %+v %+v", c.Presets, c.Clients)
	}
}
