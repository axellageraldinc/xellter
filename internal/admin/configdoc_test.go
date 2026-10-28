package admin

import (
	"strings"
	"testing"

	"github.com/axellageraldinc/xellter/internal/config"
)

func TestAnnotatedYAMLKeepsCommentsAndParses(t *testing.T) {
	cfg := &config.Config{Clients: []config.Client{{Name: "a", Presets: map[string]config.Preset{"p": {Category: "chat"}}}}}
	b, err := annotatedYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"# Routing is the same", "# How the chat preset is scored", "# How the pool is ordered"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}
