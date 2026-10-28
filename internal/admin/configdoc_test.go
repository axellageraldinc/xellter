package admin

import (
	"strings"
	"testing"

	"github.com/axellageraldinc/xellter/internal/config"
)

func TestAnnotatedYAMLHasCommentsAndNoClients(t *testing.T) {
	cfg := config.Default()
	cfg.Clients = []config.Client{{Name: "secret-client"}}
	b, err := annotatedYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"# Routing is the same", "# Seconds to wait for kenari"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "secret-client") || strings.Contains(s, "clients:") {
		t.Errorf("clients must not be in the Config tab text:\n%s", s)
	}
}
