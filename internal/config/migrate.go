package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// legacyConfig is the pre-rewrite shape, read only to migrate it. It is deliberately a private copy: the
// old structs are gone from the package so nothing can accidentally depend on them again.
type legacyConfig struct {
	Server  Server `yaml:"server"`
	Kenari  Kenari `yaml:"kenari"`
	Clients []struct {
		Name    string `yaml:"name"`
		Enabled bool   `yaml:"enabled"`
		KeyHash string `yaml:"key_hash"`
		KeyHint string `yaml:"key_hint"`
		Keys    []struct {
			Name   string `yaml:"name"`
			KeyEnv string `yaml:"key_env"`
		} `yaml:"keys"`
		Note    string   `yaml:"note"`
		Seeded  []string `yaml:"seeded"`
		Presets map[string]struct {
			Modality string   `yaml:"modality"`
			Models   []string `yaml:"models"`
		} `yaml:"presets"`
	} `yaml:"clients"`
}

// Migrate converts a pre-rewrite configuration into the current shape. It is lossy on purpose: the removed
// knobs have no equivalent, and a preset's pool starts empty unless the old preset was a combo with an
// explicit model list. The caller decides whether to write the result.
func Migrate(data []byte) (*Config, string, error) {
	var old legacyConfig
	if err := yaml.Unmarshal(data, &old); err != nil {
		return nil, "", fmt.Errorf("parse the old config: %w", err)
	}
	c := Default()
	c.Server, c.Kenari = old.Server, old.Kenari
	var notes []string
	for _, oc := range old.Clients {
		cl := Client{Name: oc.Name, Enabled: oc.Enabled, Purpose: "general", KeyHash: oc.KeyHash, KeyHint: oc.KeyHint, Note: oc.Note,
			Presets: map[string]Preset{}}
		for _, k := range oc.Keys {
			cl.Keys = append(cl.Keys, Upstream{Name: k.Name, KeyEnv: k.KeyEnv})
		}
		names := make([]string, 0, len(oc.Presets))
		for n := range oc.Presets {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			op := oc.Presets[n]
			category := op.Modality
			if CategoryOfPreset(n) == "" {
				notes = append(notes, fmt.Sprintf("client %s: preset %q has no category in the new shape and was dropped", cl.Name, n))
				continue
			}
			if category == "" {
				category = CategoryOfPreset(n)
			}
			p := Preset{Category: category, Optimization: "balanced", Models: append([]string(nil), op.Models...)}
			cl.Presets[n] = p
			if len(p.Models) == 0 {
				notes = append(notes, fmt.Sprintf("client %s: preset %q has an empty pool; pick its models in the panel", cl.Name, n))
			}
		}
		c.Clients = append(c.Clients, cl)
	}
	c.fillDefaults()
	if err := c.Validate(); err != nil {
		return nil, "", err
	}
	if len(notes) == 0 {
		notes = append(notes, "no manual follow-up needed")
	}
	return c, strings.Join(notes, "\n"), nil
}
