// Package config loads, validates and persists the xellter configuration.
//
// The configuration is one YAML file. Secrets (upstream API keys) are never
// part of it: upstreams reference an environment variable name instead, and the
// admin panel can store values in a separate 0600 secrets file.
//
// The shape is deliberately small. A client has a purpose and at most one
// preset per category; a preset is a list of models plus one optimization. Every
// other routing rule is a constant (see docs/rewrite-plan.md).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole runtime configuration. JSON tags mirror YAML tags so the
// admin panel can round-trip it.
type Config struct {
	Server  Server   `yaml:"server" json:"server"`
	Kenari  Kenari   `yaml:"kenari" json:"kenari"`
	Routing Routing  `yaml:"routing" json:"routing"`
	Clients []Client `yaml:"clients" json:"clients"`
}

// Server holds listener and storage settings.
type Server struct {
	Listen       string   `yaml:"listen" json:"listen"`
	DataDir      string   `yaml:"data_dir" json:"data_dir"`
	SecretsFile  string   `yaml:"secrets_file" json:"secrets_file"`
	AdminHosts   []string `yaml:"admin_hosts" json:"admin_hosts"` // Host header values allowed to reach the panel
	MaxBodyMB    int      `yaml:"max_body_mb" json:"max_body_mb"`
	LogRetention int      `yaml:"log_retention_days" json:"log_retention_days"`
	TrustedProxy bool     `yaml:"trusted_proxy" json:"trusted_proxy"` // honour X-Forwarded-Proto/For (tailscale serve, cloudflared)
	SessionHours int      `yaml:"session_hours" json:"session_hours"`
	AuthRecord   string   `yaml:"auth_record" json:"auth_record"` // path to the admin login record
}

// Kenari describes the upstream gateway and how often its catalog is synced.
type Kenari struct {
	BaseURL         string `yaml:"base_url" json:"base_url"`
	SyncMinutes     int    `yaml:"sync_minutes" json:"sync_minutes"`
	UserAgent       string `yaml:"user_agent" json:"user_agent"`
	FirstByteSecs   int    `yaml:"first_byte_timeout_s" json:"first_byte_timeout_s"`
	MusicTimeoutSec int    `yaml:"music_timeout_s" json:"music_timeout_s"`
	// AccountKeyEnv names the secret holding an optional full-access key of the
	// kenari account. It is only used to read plan quota and balance, never to serve requests.
	AccountKeyEnv string `yaml:"account_key_env" json:"account_key_env"`
}

// Routing is the small set of knobs that survived the rewrite. Everything a
// preset or a model decides is not here.
type Routing struct {
	RankRefreshMinutes int  `yaml:"rank_refresh_minutes" json:"rank_refresh_minutes"` // 0 = never refresh on a timer
	TopModels          int  `yaml:"top_models" json:"top_models"`                     // how many models a preset routes against
	EvictErrors        int  `yaml:"evict_errors" json:"evict_errors"`                 // errors that take a model out of a preset
	EvictWindowMin     int  `yaml:"evict_window_minutes" json:"evict_window_minutes"`
	EvictCooldownMin   int  `yaml:"evict_cooldown_minutes" json:"evict_cooldown_minutes"`
	AvoidSunsetDays    int  `yaml:"avoid_sunset_days" json:"avoid_sunset_days"`
	StickySession      bool `yaml:"sticky_session" json:"sticky_session"` // one model per session (ADR 0020)
}

// Upstream is one kenari API key of a client. Only a name and the secret
// name are configurable: xellter detects the account, the plan and the limits
// by itself (ADR 0012).
type Upstream struct {
	Name   string `yaml:"name" json:"name"`
	KeyEnv string `yaml:"key_env" json:"key_env"`
}

// Client is an app that calls xellter (Babu, aixell, polifin, ...). It has one
// xellter API key (stored only as a hash), its own kenari keys, one purpose that
// decides how its text preset is scored, and at most one preset per category.
type Client struct {
	Name    string            `yaml:"name" json:"name"`
	Enabled bool              `yaml:"enabled" json:"enabled"`
	Purpose string            `yaml:"purpose" json:"purpose"` // general | coding | agentic | vision | bulk
	KeyHash string            `yaml:"key_hash" json:"key_hash"`
	KeyHint string            `yaml:"key_hint" json:"key_hint"`
	Keys    []Upstream        `yaml:"keys" json:"keys"`       // kenari API keys of this client
	Presets map[string]Preset `yaml:"presets" json:"presets"` // preset name (one per category) -> preset
	Note    string            `yaml:"note" json:"note"`
}

// Preset is a virtual model exposed to clients: a chosen pool of real models
// plus the rule that orders them. The name is the owner's own (ADR 0021): it is
// the model name the client asks for, so it must be unique inside a client. The
// category is what says what kind of model it serves, and a client holds at
// most one preset per category.
type Preset struct {
	Category     string   `yaml:"category" json:"category"`         // chat | images | videos | audio_speech | audio_transcription | music | systemone
	Optimization string   `yaml:"optimization" json:"optimization"` // cheapest | balanced | best_quality
	Models       []string `yaml:"models" json:"models"`             // the pool, chosen in the panel
}

// Categories are the endpoint kinds a preset can serve, in display order.
var Categories = []string{"chat", "images", "videos", "audio_speech", "audio_transcription", "music", "systemone"}

// PresetName is the name xellter suggests for a new preset of a category. It
// is a suggestion, not a rule: the owner may name a preset anything (ADR 0021).
func PresetName(category string) string {
	return map[string]string{
		"chat": "text", "images": "image", "videos": "video", "audio_speech": "tts",
		"audio_transcription": "stt", "music": "music", "systemone": "systemone",
	}[category]
}

// CategoryOfPreset is the category of the old fixed preset names. It is used to
// fill the category of a config written before presets could be renamed, and
// to recognise those names in the migration; a new preset states its category
// instead.
func CategoryOfPreset(name string) string {
	for _, c := range Categories {
		if PresetName(c) == name {
			return c
		}
	}
	return ""
}

// Purposes are the client purposes that change how a text preset is scored.
var Purposes = []string{"general", "coding", "agentic", "vision", "bulk"}

// Optimizations are the three preset rules.
var Optimizations = []string{"cheapest", "balanced", "best_quality"}

// Default returns the built-in configuration (no upstreams, no clients).
func Default() *Config {
	return &Config{
		Server: Server{
			Listen: "127.0.0.1:8480", DataDir: "data", MaxBodyMB: 64, LogRetention: 30,
			TrustedProxy: true, SessionHours: 12,
			AdminHosts: []string{"localhost", "127.0.0.1"},
		},
		Kenari: Kenari{
			BaseURL: "https://kenari.id", SyncMinutes: 10, UserAgent: "xellter/1.0",
			FirstByteSecs: 60, MusicTimeoutSec: 900, AccountKeyEnv: "KENARI_ACCOUNT_KEY",
		},
		Routing: Routing{
			RankRefreshMinutes: 5, TopModels: 10, EvictErrors: 3, EvictWindowMin: 60,
			EvictCooldownMin: 720, AvoidSunsetDays: 3, StickySession: true,
		},
	}
}

// DefaultPreset is the preset a new client gets for a category: an empty pool
// and Balanced, so the owner only has to pick models.
func DefaultPreset(category string) Preset {
	return Preset{Category: category, Optimization: "balanced", Models: []string{}}
}

// Parse decodes YAML on top of defaults and validates the result.
func Parse(data []byte) (*Config, error) {
	if err := checkRetired(data); err != nil {
		return nil, err
	}
	c := Default()
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	c.fillDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Normalize fills defaults and validates a config built outside Parse (for
// example decoded from the admin panel's JSON).
func Normalize(c *Config) error {
	c.fillDefaults()
	return c.Validate()
}

// checkRetired rejects configuration that still asks for something xellter no
// longer does. Unknown keys are otherwise ignored, so a retired knob would look
// like it still works; here it is an error that names the reason.
func checkRetired(data []byte) error {
	var probe struct {
		Rules      any `yaml:"rules"`
		Resilience any `yaml:"resilience"`
		RTK        any `yaml:"rtk"`
		Benchmarks any `yaml:"benchmarks"`
		Clients    []struct {
			Presets map[string]map[string]any `yaml:"presets"`
		} `yaml:"clients"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil // the real decode reports the syntax error with a better message
	}
	for key, val := range map[string]any{
		"rules": probe.Rules, "resilience": probe.Resilience, "rtk": probe.RTK, "benchmarks": probe.Benchmarks,
	} {
		if val != nil {
			return fmt.Errorf("%s: retired by the rewrite (ADR 0017); routing rules are constants and RTK is always on, see docs/rewrite-plan.md", key)
		}
	}
	for _, cl := range probe.Clients {
		for name, p := range cl.Presets {
			for _, retired := range []string{"weights", "require", "min_quality", "max_candidates", "strategy", "rr_band", "mode", "cost_profile", "description"} {
				if _, ok := p[retired]; ok {
					return fmt.Errorf("presets.%s.%s: retired by the rewrite (ADR 0017); a preset is a category, an optimization and a model list", name, retired)
				}
			}
		}
	}
	return nil
}

func (c *Config) fillDefaults() {
	d := Default()
	if c.Server.Listen == "" {
		c.Server.Listen = d.Server.Listen
	}
	if c.Server.DataDir == "" {
		c.Server.DataDir = d.Server.DataDir
	}
	if c.Server.MaxBodyMB <= 0 {
		c.Server.MaxBodyMB = d.Server.MaxBodyMB
	}
	if c.Server.LogRetention <= 0 {
		c.Server.LogRetention = d.Server.LogRetention
	}
	if c.Server.SessionHours <= 0 {
		c.Server.SessionHours = d.Server.SessionHours
	}
	if c.Kenari.BaseURL == "" {
		c.Kenari.BaseURL = d.Kenari.BaseURL
	}
	c.Kenari.BaseURL = strings.TrimRight(c.Kenari.BaseURL, "/")
	if c.Kenari.SyncMinutes <= 0 {
		c.Kenari.SyncMinutes = d.Kenari.SyncMinutes
	}
	if c.Kenari.UserAgent == "" {
		c.Kenari.UserAgent = d.Kenari.UserAgent
	}
	if c.Kenari.FirstByteSecs <= 0 {
		c.Kenari.FirstByteSecs = d.Kenari.FirstByteSecs
	}
	if c.Kenari.MusicTimeoutSec <= 0 {
		c.Kenari.MusicTimeoutSec = d.Kenari.MusicTimeoutSec
	}
	if c.Kenari.AccountKeyEnv == "" {
		c.Kenari.AccountKeyEnv = d.Kenari.AccountKeyEnv
	}
	r := &c.Routing
	if r.TopModels <= 0 {
		r.TopModels = d.Routing.TopModels
	}
	if r.EvictErrors <= 0 {
		r.EvictErrors = d.Routing.EvictErrors
	}
	if r.EvictWindowMin <= 0 {
		r.EvictWindowMin = d.Routing.EvictWindowMin
	}
	if r.EvictCooldownMin <= 0 {
		r.EvictCooldownMin = d.Routing.EvictCooldownMin
	}
	if r.RankRefreshMinutes < 0 {
		r.RankRefreshMinutes = 0
	}
	for i := range c.Clients {
		cl := &c.Clients[i]
		if cl.Purpose == "" {
			cl.Purpose = "general"
		}
		for name, p := range cl.Presets {
			if p.Category == "" {
				p.Category = CategoryOfPreset(name)
			}
			if p.Optimization == "" {
				p.Optimization = "balanced"
			}
			if p.Models == nil {
				p.Models = []string{}
			}
			cl.Presets[name] = p
		}
	}
}

var (
	nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
	hashRe = regexp.MustCompile(`^[a-f0-9]{64}$`)
	envRe  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

// Validate checks structural consistency and returns every problem found.
func (c *Config) Validate() error {
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if !strings.Contains(c.Server.Listen, ":") {
		bad("server.listen must be host:port")
	}
	cseen, envSeen := map[string]bool{}, map[string]string{}
	for i, cl := range c.Clients {
		if !nameRe.MatchString(cl.Name) {
			bad("clients[%d].name is invalid", i)
		}
		if cseen[cl.Name] {
			bad("duplicate client %q", cl.Name)
		}
		cseen[cl.Name] = true
		if cl.KeyHash != "" && !hashRe.MatchString(cl.KeyHash) {
			bad("client %q: key_hash must be sha256 hex", cl.Name)
		}
		// An empty purpose is the general one: a client created before the field existed must keep working.
		if cl.Purpose != "" && !contains(Purposes, cl.Purpose) {
			bad("client %q: purpose must be one of %v", cl.Name, Purposes)
		}
		kseen := map[string]bool{}
		for _, u := range cl.Keys {
			if !nameRe.MatchString(u.Name) {
				bad("client %q: key name %q is invalid (letters, digits, dot, dash, underscore)", cl.Name, u.Name)
			}
			if kseen[u.Name] {
				bad("client %q: duplicate key %q", cl.Name, u.Name)
			}
			kseen[u.Name] = true
			if !envRe.MatchString(u.KeyEnv) {
				bad("client %q key %q: secret name must look like KENARI_KEY_X", cl.Name, u.Name)
			}
			if other, dup := envSeen[u.KeyEnv]; dup {
				bad("secret name %s is used by both %s and %s/%s", u.KeyEnv, other, cl.Name, u.Name)
			}
			envSeen[u.KeyEnv] = cl.Name + "/" + u.Name
		}
		cats := map[string]string{} // category -> preset name, to catch a second preset of a kind
		for name, p := range cl.Presets {
			where := "client " + cl.Name + " preset " + name
			if !nameRe.MatchString(name) {
				bad("%s: invalid name", where)
			}
			category := p.Category
			if category == "" {
				// A preset written before presets could be renamed still carries the fixed name; that
				// name is its category. A new preset must state its category.
				category = CategoryOfPreset(name)
				if category == "" {
					bad("%s: no category; state one of %v", where, Categories)
					continue
				}
			}
			if !contains(Categories, category) {
				bad("%s: category must be one of %v", where, Categories)
				continue
			}
			// The old fixed names keep their meaning, so a preset called "text" is a text
			// preset. Any other name is the owner's own and states its category.
			if legacy := CategoryOfPreset(name); legacy != "" && legacy != category {
				bad("%s: %q is a %s preset; pick another name for a %s preset", where, name, legacy, category)
				continue
			}
			if other, dup := cats[category]; dup {
				bad("%s: a client may hold only one %s preset (already %q)", where, category, other)
			}
			cats[category] = name
			if !contains(Optimizations, p.Optimization) {
				bad("%s: optimization must be one of %v", where, Optimizations)
			}
			seen := map[string]bool{}
			for _, m := range p.Models {
				if strings.TrimSpace(m) == "" {
					bad("%s: empty model id", where)
					continue
				}
				if seen[m] {
					bad("%s: model %q listed twice", where, m)
				}
				seen[m] = true
			}
		}
	}
	if c.Kenari.AccountKeyEnv != "" && !envRe.MatchString(c.Kenari.AccountKeyEnv) {
		bad("kenari.account_key_env must look like KENARI_ACCOUNT_KEY")
	}
	if c.Routing.TopModels < 1 || c.Routing.TopModels > 50 {
		bad("routing.top_models must be 1..50")
	}
	if c.Routing.EvictWindowMin < 1 || c.Routing.EvictCooldownMin < 1 {
		bad("routing.evict_window_minutes and evict_cooldown_minutes must be >= 1")
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// MatchAny reports whether name matches any glob ("*" matches everything).
func MatchAny(globs []string, name string) bool {
	for _, g := range globs {
		if g == "*" {
			return true
		}
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

// Load reads and parses a config file. A missing file yields the defaults.
func Load(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		data = nil
	} else if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	base := filepath.Dir(file)
	c.Server.DataDir = absFrom(base, c.Server.DataDir)
	if c.Server.SecretsFile == "" {
		c.Server.SecretsFile = filepath.Join(c.Server.DataDir, "secrets.env")
	} else {
		c.Server.SecretsFile = absFrom(base, c.Server.SecretsFile)
	}
	if c.Server.AuthRecord == "" {
		c.Server.AuthRecord = filepath.Join(c.Server.DataDir, "auth-record.json")
	} else {
		c.Server.AuthRecord = absFrom(base, c.Server.AuthRecord)
	}
	return c, nil
}

func absFrom(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// Save writes the config atomically (mode 0600) and keeps a history copy of
// the previous file under <data_dir>/config-history.
func Save(file string, c *Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(file); err == nil {
		dir := filepath.Join(c.Server.DataDir, "config-history")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			t := time.Now().UTC()
			name := filepath.Join(dir, t.Format("20060102-150405.000")+".yaml")
			for _, err := os.Stat(name); err == nil; _, err = os.Stat(name) { // saves within one millisecond must not overwrite each other
				t = t.Add(time.Millisecond)
				name = filepath.Join(dir, t.Format("20060102-150405.000")+".yaml")
			}
			_ = os.WriteFile(name, old, 0o600)
			pruneHistory(dir, 50)
		}
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append([]byte("# Managed by xellter (the admin panel rewrites this file; comments are not preserved).\n"), out...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func pruneHistory(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= keep {
		return
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// History lists saved config versions, newest first.
func History(dataDir string) []string {
	ents, _ := os.ReadDir(filepath.Join(dataDir, "config-history"))
	var names []string
	for _, e := range ents {
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

// ReadHistory returns one saved version's YAML.
func ReadHistory(dataDir, id string) ([]byte, error) {
	if !regexp.MustCompile(`^[0-9]{8}-[0-9]{6}\.[0-9]{3}$`).MatchString(id) {
		return nil, errors.New("bad history id")
	}
	return os.ReadFile(filepath.Join(dataDir, "config-history", id+".yaml"))
}

// ---- secrets ----

// LoadSecrets reads KEY=VALUE lines from the secrets file; the process
// environment fills anything missing.
func LoadSecrets(file string) map[string]string {
	out := map[string]string{}
	if f, err := os.Open(file); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if ok {
				out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	return out
}

// SetSecret stores one secret in the secrets file (0600), replacing any old value.
func SetSecret(file, name, value string) error {
	if !envRe.MatchString(name) {
		return errors.New("bad secret name")
	}
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("bad secret value")
	}
	m := LoadSecrets(file)
	m[name] = value
	return writeSecrets(file, m)
}

// DeleteSecret removes one secret from the secrets file.
func DeleteSecret(file, name string) error {
	m := LoadSecrets(file)
	if _, ok := m[name]; !ok {
		return nil
	}
	delete(m, name)
	return writeSecrets(file, m)
}

func writeSecrets(file string, m map[string]string) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
