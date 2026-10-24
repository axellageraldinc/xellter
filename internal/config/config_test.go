package config

import (
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
}

func TestGeneratedPresetsCoverEveryKind(t *testing.T) {
	want := map[string]string{"chat": "text", "images": "image", "videos": "video", "audio_speech": "tts", "audio_transcription": "stt", "music": "music"}
	for kind, name := range want {
		if PresetName(kind) != name {
			t.Errorf("%s -> %s", kind, PresetName(kind))
		}
		p := DefaultPreset(kind)
		if p.Modality != kind || p.Mode != "auto" || p.MaxCandidates == 0 || p.Weights.Cost == 0 {
			t.Errorf("preset %s incomplete: %+v", kind, p)
		}
	}
	if p := DefaultPreset("chat"); !p.Require.ToolCall || p.MinQuality == 0 {
		t.Error("text preset must require tool calling and a quality floor")
	}
}

func TestValidationCatchesMistakes(t *testing.T) {
	good := "    keys:\n      - {name: main, key_env: KENARI_KEY_A}\n"
	cases := map[string]string{
		"bad secret name":          "clients:\n  - name: x\n    keys:\n      - {name: main, key_env: sk-live-123}\n",
		"duplicate key in client":  "clients:\n  - name: x\n    keys:\n      - {name: a, key_env: K1}\n      - {name: a, key_env: K2}\n",
		"secret shared by clients": "clients:\n  - name: x\n    keys:\n      - {name: a, key_env: K1}\n  - name: y\n    keys:\n      - {name: a, key_env: K1}\n",
		"duplicate client":         "clients:\n  - {name: x}\n  - {name: x}\n",
		"combo without models":     "clients:\n  - name: x\n" + good + "    presets:\n      p: {modality: chat, mode: combo, strategy: priority, weights: {cost: 1}}\n",
		"bad modality":             "clients:\n  - name: x\n    presets:\n      p: {modality: telepathy, mode: auto, strategy: priority, weights: {cost: 1}}\n",
		"negative weight":          "clients:\n  - name: x\n    presets:\n      p: {modality: chat, mode: auto, strategy: priority, weights: {cost: -1}}\n",
		"bad preset name":          "clients:\n  - name: x\n    presets:\n      'a b': {modality: chat, mode: auto, strategy: priority, weights: {cost: 1}}\n",
		"canary over 100":          "rules: {canary_percent: 150}\n",
		"bad account key env":      "kenari: {account_key_env: not-an-env}\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOldSingleJudgeKeyMovesIntoTheOrder(t *testing.T) {
	c, err := Parse([]byte("jev:\n  enabled: true\n  model: clef:free\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.JEV.Judges) != 1 || c.JEV.Judges[0] != "clef:free" || c.JEV.Model != "" {
		t.Fatalf("migration: %+v", c.JEV)
	}
	if c.JEV.Model != "" {
		t.Fatal("the old key must not be written back")
	}
	// an explicit order wins, and the old key is prepended to it
	c, err = Parse([]byte("jev:\n  judges: [clef:free, jev-1-13:free]\n  model: jev-1-14\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.JEV.Judges) != 3 || c.JEV.Judges[0] != "jev-1-14" || c.JEV.Judges[1] != "clef:free" {
		t.Fatalf("judges: %v", c.JEV.Judges)
	}
	if _, err := Parse([]byte("jev:\n  judges: [\"\"]\n")); err == nil {
		t.Fatal("an empty judge id must be rejected")
	}
	if _, err := Parse([]byte("jev:\n  judges: [clef:free, clef:free]\n")); err == nil {
		t.Fatal("a repeated judge id must be rejected")
	}
	// saving writes the new shape and drops the old key
	dir := t.TempDir()
	file := filepath.Join(dir, "xellter.yaml")
	c.Server.DataDir = filepath.Join(dir, "data")
	if err := Save(file, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "judges:") || strings.Contains(string(b), "model: jev-1-14") {
		t.Fatalf("saved config:\n%s", b)
	}
}

func TestSaveKeepsHistoryAndNeverStoresSecrets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "xellter.yaml")
	c := Default()
	c.Server.DataDir = filepath.Join(dir, "data")
	c.Clients = []Client{{Name: "babu", Enabled: true, Keys: []Upstream{{Name: "main", KeyEnv: "KENARI_KEY_BABU_MAIN"}}, Presets: DefaultPresets()}}
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
	if _, err := ReadHistory(c.Server.DataDir, "../../etc/passwd"); err == nil {
		t.Fatal("history id path traversal")
	}
	back, err := Load(file)
	if err != nil || len(back.Clients[0].Presets) != 6 {
		t.Fatalf("round trip lost presets: %v %+v", err, back)
	}
}

func TestMatchAny(t *testing.T) {
	if !MatchAny([]string{"*"}, "x") || !MatchAny([]string{"deepseek-*"}, "deepseek-v4") || MatchAny([]string{"deepseek-*"}, "glm-5") || MatchAny(nil, "x") {
		t.Fatal("glob matching broken")
	}
}
