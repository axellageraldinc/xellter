package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/proxy"
	"github.com/axellageraldinc/xellter/internal/rank"
)

// twoProviders is the setup of ADR 0040: the built-in provider and a second one called "alt", each with its own
// fake upstream. The client "multi" has one key at each; "solo" has a kenari key only; "altonly" an alt key only.
func twoProviders(t *testing.T) (*env, *fake) {
	t.Helper()
	alt := newFake(t)
	e := setupWith(t, func(c *config.Config) {
		c.Providers = []config.Provider{{ID: "alt", Kenari: config.Kenari{BaseURL: alt.srv.URL}}}
		c.Clients = append(c.Clients,
			config.Client{Name: "multi", Enabled: true, Purpose: "general", KeyHash: proxy.HashKey("xl-multi"),
				Keys: []config.Upstream{{Name: "k", KeyEnv: "KEY_MULTI"}, {Name: "a", KeyEnv: "ALT_KEY_MULTI", Provider: "alt"}},
				Presets: map[string]config.Preset{
					"own":   {Category: "chat", Optimization: "cheapest", Models: []string{"cheap-b"}, Excluded: noAuto},
					"other": {Category: "chat", Optimization: "cheapest", Models: []string{"alt/cheap-b"}, Excluded: noAuto},
				}},
			config.Client{Name: "solo", Enabled: true, Purpose: "general", KeyHash: proxy.HashKey("xl-solo"),
				Keys:    []config.Upstream{{Name: "k", KeyEnv: "KEY_SOLO"}},
				Presets: map[string]config.Preset{"other": {Category: "chat", Optimization: "cheapest", Models: []string{"alt/cheap-b"}}}},
		)
	}, func(f *fake) catalog.Source {
		return catalog.Merge(
			catalog.Named{ID: "kenari", Src: &catalog.Kenari{BaseURL: f.srv.URL, UA: "test", HTTP: f.srv.Client()}},
			catalog.Named{ID: "alt", Src: &catalog.Kenari{BaseURL: alt.srv.URL, UA: "test", HTTP: alt.srv.Client()}})
	})
	for k, v := range map[string]string{"KEY_MULTI": "secret-multi", "ALT_KEY_MULTI": "secret-alt", "KEY_SOLO": "secret-solo"} {
		if err := e.a.SetSecret(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return e, alt
}

// lastFor is the latest call for a model. The background key probes call the fakes too, so "the last call" is not
// the request a test just made.
func lastFor(f *fake, model string) call {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Model == model {
			return f.calls[i]
		}
	}
	return call{}
}

// noAuto unticks the free and discounted models of both providers, so a preset routes over what it lists.
var noAuto = []string{"free-a:free", "promo-d", "alt/free-a:free", "alt/promo-d"}

const hi = `{"messages":[{"role":"user","content":"hi"}]}`

// A model of the built-in provider is served by its upstream with its key, and a model of another provider by
// that provider's upstream, under the name that provider knows, with that provider's key.
func TestEachModelGoesToItsOwnProvider(t *testing.T) {
	e, alt := twoProviders(t)

	resp, body := e.post(t, "xl-multi", "/v1/chat/completions", strings.Replace(hi, "{", `{"model":"own",`, 1))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own: %d %s", resp.StatusCode, body)
	}
	if got := lastFor(e.f, "cheap-b"); got.Model != "cheap-b" || got.Auth != "Bearer secret-multi" {
		t.Fatalf("kenari saw %+v", got)
	}
	if alt.count("cheap-b") != 0 {
		t.Fatal("the other provider was called for a model of kenari")
	}

	before := e.f.count("cheap-b")
	resp, body = e.post(t, "xl-multi", "/v1/chat/completions", strings.Replace(hi, "{", `{"model":"other",`, 1))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("other: %d %s", resp.StatusCode, body)
	}
	if got := lastFor(alt, "cheap-b"); got.Model != "cheap-b" || got.Auth != "Bearer secret-alt" {
		t.Fatalf("the second provider saw %+v: it must get the bare model name and its own key", got)
	}
	if e.f.count("cheap-b") != before {
		t.Fatal("kenari was called for a model of the second provider")
	}
	if got := resp.Header.Get("X-Xellter-Model"); got != "alt/cheap-b" {
		t.Fatalf("the model that served is named %q, want alt/cheap-b", got)
	}
}

// A key is never used for another provider's model: a client with only a kenari key does not get a model of the
// second provider, however its preset lists it. The free models of its own provider still join the pool (ADR
// 0022), so the request is answered, but never by the model the key cannot call.
func TestAKeyOfAnotherProviderIsNotUsed(t *testing.T) {
	e, alt := twoProviders(t)
	resp, body := e.post(t, "xl-solo", "/v1/chat/completions", strings.Replace(hi, "{", `{"model":"other",`, 1))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Xellter-Model"); got != "free-a:free" {
		t.Fatalf("served by %q, want the free model of its own provider", got)
	}
	if alt.count("cheap-b") != 0 || alt.count("free-a:free") != 0 {
		t.Fatal("a request reached the second provider although the client has no key there")
	}
	rk := e.a.Rt.Rankings()[rank.Key("solo", "other")]
	if rk == nil {
		t.Fatal("no ranking")
	}
	found := false
	for _, r := range rk.Rejected {
		if r.Model == "alt/cheap-b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the model without a key must be rejected with a reason: %+v", rk.Rejected)
	}
}

// A file written before providers existed loads unchanged: no providers section, keys without a provider.
func TestAnOldConfigHasOnlyTheBuiltInProvider(t *testing.T) {
	cfg := config.Default()
	if err := config.Normalize(cfg); err != nil {
		t.Fatal(err)
	}
	list := cfg.ProviderList()
	if len(list) != 1 || list[0].ID != "kenari" || list[0].BaseURL != "https://kenari.id" {
		t.Fatalf("providers: %+v", list)
	}
	if got := (config.Upstream{Name: "k", KeyEnv: "X"}).ProviderID(); got != "kenari" {
		t.Fatalf("a key without a provider belongs to %q", got)
	}
}

// A key naming a provider that is not configured is refused when the configuration loads, not at request time.
func TestAKeyOfAnUnknownProviderIsRefused(t *testing.T) {
	cfg := config.Default()
	cfg.Clients = []config.Client{{Name: "x", Enabled: true, Keys: []config.Upstream{{Name: "k", KeyEnv: "K", Provider: "nope"}}}}
	if err := config.Normalize(cfg); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("got %v", err)
	}
	cfg = config.Default()
	cfg.Providers = []config.Provider{{ID: "kenari", Kenari: config.Kenari{BaseURL: "http://x"}}}
	if err := config.Normalize(cfg); err == nil {
		t.Fatal("the id of the built-in provider must be refused for a second one")
	}
}
