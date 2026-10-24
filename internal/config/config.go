// Package config loads, validates and persists the xellter configuration.
//
// The configuration is one YAML file. Secrets (upstream API keys) are never
// part of it: upstreams reference an environment variable name instead, and the
// admin panel can store values in a separate 0600 secrets file.
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
	Server     Server             `yaml:"server" json:"server"`
	Kenari     Kenari             `yaml:"kenari" json:"kenari"`
	Clients    []Client           `yaml:"clients" json:"clients"`
	Rules      Rules              `yaml:"rules" json:"rules"`
	Resilience Resilience         `yaml:"resilience" json:"resilience"`
	JEV        JEV                `yaml:"jev" json:"jev"`
	RTK        RTK                `yaml:"rtk" json:"rtk"`
	Benchmarks map[string]float64 `yaml:"benchmarks" json:"benchmarks"`
}

// Server holds listener and storage settings.
type Server struct {
	Listen       string   `yaml:"listen" json:"listen"`
	DataDir      string   `yaml:"data_dir" json:"data_dir"`
	SecretsFile  string   `yaml:"secrets_file" json:"secrets_file"`
	AdminHosts   []string `yaml:"admin_hosts" json:"admin_hosts"` // Host header values allowed to reach /admin
	MaxBodyMB    int      `yaml:"max_body_mb" json:"max_body_mb"`
	LogRetention int      `yaml:"log_retention_days" json:"log_retention_days"`
	LogRequests  bool     `yaml:"log_requests" json:"log_requests"`
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

// Upstream is one kenari API key of a client. Only a name and the secret
// name are configurable: xellter detects the account, the plan and the limits
// by itself (ADR 0012).
type Upstream struct {
	Name   string `yaml:"name" json:"name"`
	KeyEnv string `yaml:"key_env" json:"key_env"`
}

// Client is an app that calls xellter (Babu, aixell, polifin, ...). It has one
// xellter API key (stored only as a hash), its own kenari keys and its own
// presets, which xellter generates from the models those keys can use.
type Client struct {
	Name        string            `yaml:"name" json:"name"`
	Enabled     bool              `yaml:"enabled" json:"enabled"`
	KeyHash     string            `yaml:"key_hash" json:"key_hash"`
	KeyHint     string            `yaml:"key_hint" json:"key_hint"`
	Keys        []Upstream        `yaml:"keys" json:"keys"`                         // kenari API keys of this client
	Presets     map[string]Preset `yaml:"presets" json:"presets"`                   // name -> preset; generated automatically, editable
	Seeded      []string          `yaml:"seeded" json:"seeded"`                     // modalities already offered a generated preset
	Passthrough []string          `yaml:"passthrough" json:"passthrough"`           // real model globs allowed directly; empty = none
	DailyBudget float64           `yaml:"daily_budget_idr" json:"daily_budget_idr"` // 0 = unlimited
	RTK         *bool             `yaml:"rtk" json:"rtk"`                           // nil = follow global
	Note        string            `yaml:"note" json:"note"`
}

// Weights are relative weights of the ranking components.
type Weights struct {
	Cost        float64 `yaml:"cost" json:"cost"`
	Benchmark   float64 `yaml:"benchmark" json:"benchmark"`
	JEV         float64 `yaml:"jev" json:"jev"`
	Reliability float64 `yaml:"reliability" json:"reliability"`
	Context     float64 `yaml:"context" json:"context"`
}

// Require are hard capability gates. There is no context-size gate: every
// model the client's keys can call may serve a preset (ADR 0012).
type Require struct {
	ToolCall  bool `yaml:"tool_call" json:"tool_call"`
	Vision    bool `yaml:"vision" json:"vision"`
	Reasoning bool `yaml:"reasoning" json:"reasoning"`
}

// CostProfile is the reference request used to turn prices into one number.
type CostProfile struct {
	InputTokens  int     `yaml:"input_tokens" json:"input_tokens"`
	OutputTokens int     `yaml:"output_tokens" json:"output_tokens"`
	CachedRatio  float64 `yaml:"cached_ratio" json:"cached_ratio"`
	Images       float64 `yaml:"images" json:"images"`
	Seconds      float64 `yaml:"seconds" json:"seconds"`
	Chars        float64 `yaml:"chars" json:"chars"`
	Variant      string  `yaml:"variant" json:"variant"`
}

// Preset is a virtual model exposed to clients. Mode "auto" ranks the catalog;
// mode "combo" uses Models as an explicit ordered list. A preset has no model
// allow or deny list: the models the client's kenari keys may call are the
// ones it can use (ADR 0012).
type Preset struct {
	Description   string      `yaml:"description" json:"description"`
	Modality      string      `yaml:"modality" json:"modality"` // chat | images | videos | audio_speech | audio_transcription | music
	Mode          string      `yaml:"mode" json:"mode"`         // auto | combo
	Models        []string    `yaml:"models" json:"models"`     // combo: ordered list
	Require       Require     `yaml:"require" json:"require"`
	MinQuality    float64     `yaml:"min_quality" json:"min_quality"`
	MaxCandidates int         `yaml:"max_candidates" json:"max_candidates"`
	Strategy      string      `yaml:"strategy" json:"strategy"` // round_robin | priority
	RRBand        float64     `yaml:"rr_band" json:"rr_band"`   // score distance treated as a tie
	Weights       Weights     `yaml:"weights" json:"weights"`
	Cost          CostProfile `yaml:"cost_profile" json:"cost_profile"`
}

// Rules are the auto-route policy knobs shared by all auto presets.
type Rules struct {
	FreeBonus            float64 `yaml:"free_bonus" json:"free_bonus"`
	PlanBonus            float64 `yaml:"plan_bonus" json:"plan_bonus"`
	PlanCostFactor       float64 `yaml:"plan_cost_factor" json:"plan_cost_factor"` // share of list price a plan-covered request counts for
	PromoBonusMax        float64 `yaml:"promo_bonus_max" json:"promo_bonus_max"`
	PromoMinRemainingHrs float64 `yaml:"promo_min_remaining_hours" json:"promo_min_remaining_hours"`
	UpcomingLookaheadHrs float64 `yaml:"upcoming_lookahead_hours" json:"upcoming_lookahead_hours"`
	NewModelDays         int     `yaml:"new_model_days" json:"new_model_days"`
	CanaryPercent        float64 `yaml:"canary_percent" json:"canary_percent"`
	CanaryMinRequests    int     `yaml:"canary_min_requests" json:"canary_min_requests"`
	CanaryMinSuccess     float64 `yaml:"canary_min_success" json:"canary_min_success"`
	AvoidSunsetDays      int     `yaml:"avoid_sunset_days" json:"avoid_sunset_days"`
	StickyMinutes        int     `yaml:"sticky_minutes" json:"sticky_minutes"`
	StatsWindowHours     int     `yaml:"stats_window_hours" json:"stats_window_hours"`
}

// Resilience tunes retries, cooldowns and the circuit breaker.
type Resilience struct {
	MaxAttempts     int `yaml:"max_attempts" json:"max_attempts"`
	CooldownBaseSec int `yaml:"cooldown_base_s" json:"cooldown_base_s"`
	CooldownMaxSec  int `yaml:"cooldown_max_s" json:"cooldown_max_s"`
	BreakerFailures int `yaml:"breaker_failures" json:"breaker_failures"`
	BreakerOpenSec  int `yaml:"breaker_open_s" json:"breaker_open_s"`
}

// JEV configures the System One quality judge.
type JEV struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Judges is the ordered chain of System One models that score an answer: the
	// first one is used, the next one takes over when it is unavailable at
	// kenari. Any other free System One judge is appended as a last resort.
	Judges []string `yaml:"judges" json:"judges"`
	// Model is the single-judge setting from before the chain existed. It is
	// migrated into Judges on load and never written back.
	Model          string   `yaml:"model,omitempty" json:"model,omitempty"`
	DailyBudgetIDR float64  `yaml:"daily_budget_idr" json:"daily_budget_idr"`
	EveryHours     int      `yaml:"every_hours" json:"every_hours"`
	PassThreshold  float64  `yaml:"pass_threshold" json:"pass_threshold"`
	Prompts        []string `yaml:"prompts" json:"prompts"`
}

// RTK configures the tool-output compressor.
type RTK struct {
	Enabled     bool `yaml:"enabled" json:"enabled"`
	MinChars    int  `yaml:"min_chars" json:"min_chars"`
	MaxChars    int  `yaml:"max_chars" json:"max_chars"`
	GitDiff     bool `yaml:"git_diff" json:"git_diff"`
	Grep        bool `yaml:"grep" json:"grep"`
	LS          bool `yaml:"ls" json:"ls"`
	Logs        bool `yaml:"logs" json:"logs"`
	Truncate    bool `yaml:"truncate" json:"truncate"`
	DiffContext int  `yaml:"diff_context" json:"diff_context"`
}

// Modalities are the endpoint kinds a preset can serve.
var Modalities = []string{"chat", "images", "videos", "audio_speech", "audio_transcription", "music"}

// DefaultJEVJudge is the judge used when the config names none.
const DefaultJEVJudge = "jev-1-13:free"

// Default returns the built-in configuration (no upstreams, no clients).
func Default() *Config {
	c := &Config{
		Server: Server{
			Listen: "127.0.0.1:8480", DataDir: "data", MaxBodyMB: 64, LogRetention: 30,
			LogRequests: true, TrustedProxy: true, SessionHours: 12,
			AdminHosts: []string{"localhost", "127.0.0.1"},
		},
		Kenari: Kenari{BaseURL: "https://kenari.id", SyncMinutes: 10, UserAgent: "xellter/1.0", FirstByteSecs: 60, MusicTimeoutSec: 900, AccountKeyEnv: "KENARI_ACCOUNT_KEY"},
		Rules: Rules{
			FreeBonus: 0.10, PlanBonus: 0.08, PlanCostFactor: 0.3, PromoBonusMax: 0.05, PromoMinRemainingHrs: 12, UpcomingLookaheadHrs: 24,
			NewModelDays: 14, CanaryPercent: 5, CanaryMinRequests: 20, CanaryMinSuccess: 0.9, AvoidSunsetDays: 3,
			StickyMinutes: 30, StatsWindowHours: 24,
		},
		Resilience: Resilience{MaxAttempts: 4, CooldownBaseSec: 30, CooldownMaxSec: 900, BreakerFailures: 5, BreakerOpenSec: 120},
		JEV: JEV{
			Enabled: false, DailyBudgetIDR: 1000, EveryHours: 24, PassThreshold: 0.6,
			Prompts: []string{
				"Explain in two sentences why the sky is blue.",
				"Tulis fungsi Python untuk membalik string tanpa slicing, lengkap dengan contoh pemakaian.",
				"Ringkas dalam tiga poin: manfaat tidur cukup bagi kesehatan.",
			},
		},
		RTK:        RTK{Enabled: true, MinChars: 2000, MaxChars: 12000, GitDiff: true, Grep: true, LS: true, Logs: true, Truncate: true, DiffContext: 1},
		Benchmarks: map[string]float64{},
	}
	return c
}

// PresetModalities lists the kinds of presets xellter generates, in display order.
var PresetModalities = []string{"chat", "images", "videos", "audio_speech", "audio_transcription", "music"}

// PresetName is the generated preset name for a modality.
func PresetName(modality string) string {
	return map[string]string{"chat": "text", "images": "image", "videos": "video", "audio_speech": "tts", "audio_transcription": "stt", "music": "music"}[modality]
}

// CheckPresetNames enforces that every preset is one of the predefined kinds under its own name (text is chat,
// image is images, ...). A client therefore never has two presets of one kind, and apps can rely on the names.
func CheckPresetNames(c *Config) error {
	for _, cl := range c.Clients {
		for name, p := range cl.Presets {
			kind := ""
			for _, m := range PresetModalities {
				if PresetName(m) == name {
					kind = m
				}
			}
			if kind == "" {
				return fmt.Errorf("client %s: %q is not a preset name; use one of text, image, video, tts, stt, music", cl.Name, name)
			}
			if p.Modality != kind {
				return fmt.Errorf("client %s: preset %q must be of kind %s", cl.Name, name, kind)
			}
		}
	}
	return nil
}

// DefaultPreset returns the generated preset for a modality.
func DefaultPreset(modality string) Preset {
	media := Weights{Cost: 70, Benchmark: 10, Reliability: 20}
	switch modality {
	case "chat":
		return Preset{Description: "Text and chat. Picks the best model that is cheap enough.", Modality: "chat", Mode: "auto", Require: Require{ToolCall: true},
			MinQuality: 0.50, MaxCandidates: 4, Strategy: "round_robin", RRBand: 0.04, Weights: Weights{Cost: 25, Benchmark: 35, JEV: 20, Reliability: 15, Context: 5},
			Cost: CostProfile{InputTokens: 4000, OutputTokens: 600, CachedRatio: 0.7}}
	case "images":
		return Preset{Description: "Image generation, free and cheap models first.", Modality: "images", Mode: "auto", MaxCandidates: 4, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{Images: 1}}
	case "videos":
		return Preset{Description: "Video generation, cheapest first.", Modality: "videos", Mode: "auto", MaxCandidates: 3, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{Seconds: 5, Variant: "720p"}}
	case "audio_speech":
		return Preset{Description: "Text to speech, cheapest first.", Modality: "audio_speech", Mode: "auto", MaxCandidates: 3, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{Chars: 1000}}
	case "audio_transcription":
		return Preset{Description: "Speech to text, cheapest first.", Modality: "audio_transcription", Mode: "auto", MaxCandidates: 3, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{Seconds: 60}}
	}
	return Preset{Description: "Music generation, cheapest first.", Modality: "music", Mode: "auto", MaxCandidates: 2, Strategy: "priority", RRBand: 0.03, Weights: media}
}

// DefaultPresets returns every generated preset keyed by name (used by tests and as a template).
func DefaultPresets() map[string]Preset {
	out := map[string]Preset{}
	for _, m := range PresetModalities {
		out[PresetName(m)] = DefaultPreset(m)
	}
	return out
}

// Parse decodes YAML on top of defaults and validates the result.
func Parse(data []byte) (*Config, error) {
	c := Default()
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if c.Benchmarks == nil {
		c.Benchmarks = map[string]float64{}
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
	if c.Benchmarks == nil {
		c.Benchmarks = map[string]float64{}
	}
	c.fillDefaults()
	return c.Validate()
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
	if c.Resilience.MaxAttempts <= 0 {
		c.Resilience.MaxAttempts = d.Resilience.MaxAttempts
	}
	if c.Resilience.CooldownBaseSec <= 0 {
		c.Resilience.CooldownBaseSec = d.Resilience.CooldownBaseSec
	}
	if c.Resilience.CooldownMaxSec <= 0 {
		c.Resilience.CooldownMaxSec = d.Resilience.CooldownMaxSec
	}
	if c.Resilience.BreakerFailures <= 0 {
		c.Resilience.BreakerFailures = d.Resilience.BreakerFailures
	}
	if c.Resilience.BreakerOpenSec <= 0 {
		c.Resilience.BreakerOpenSec = d.Resilience.BreakerOpenSec
	}
	if c.Rules.PlanCostFactor <= 0 {
		c.Rules.PlanCostFactor = d.Rules.PlanCostFactor
	}
	if c.Rules.StatsWindowHours <= 0 {
		c.Rules.StatsWindowHours = d.Rules.StatsWindowHours
	}
	if c.Rules.StickyMinutes < 0 {
		c.Rules.StickyMinutes = 0
	}
	if c.RTK.MinChars <= 0 {
		c.RTK.MinChars = d.RTK.MinChars
	}
	if c.RTK.MaxChars <= 0 {
		c.RTK.MaxChars = d.RTK.MaxChars
	}
	if c.JEV.Model != "" {
		// older configs name one judge; it becomes the head of the chain
		c.JEV.Judges = append([]string{c.JEV.Model}, c.JEV.Judges...)
		c.JEV.Model = ""
	}
	for i, id := range c.JEV.Judges {
		c.JEV.Judges[i] = strings.TrimSpace(id) // Validate reports empty and repeated ids
	}
	if len(c.JEV.Judges) == 0 {
		c.JEV.Judges = []string{DefaultJEVJudge}
	}
	if c.JEV.PassThreshold <= 0 {
		c.JEV.PassThreshold = d.JEV.PassThreshold
	}
	if c.JEV.EveryHours <= 0 {
		c.JEV.EveryHours = d.JEV.EveryHours
	}
	if len(c.JEV.Prompts) == 0 {
		c.JEV.Prompts = d.JEV.Prompts
	}
	if c.Kenari.AccountKeyEnv == "" {
		c.Kenari.AccountKeyEnv = d.Kenari.AccountKeyEnv
	}
	for i := range c.Clients {
		cl := &c.Clients[i]
		for name, s := range cl.Presets {
			if s.Mode == "" {
				s.Mode = "auto"
			}
			if s.Strategy == "" {
				s.Strategy = "round_robin"
			}
			if s.MaxCandidates <= 0 {
				s.MaxCandidates = 4
			}
			if s.RRBand < 0 {
				s.RRBand = 0
			}
			cl.Presets[name] = s
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
		checkGlobs(&errs, "client "+cl.Name+".passthrough", cl.Passthrough)
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
		for name, s := range cl.Presets {
			where := "client " + cl.Name + " preset " + name
			if !nameRe.MatchString(name) {
				bad("%s: invalid name", where)
			}
			if !contains(Modalities, s.Modality) {
				bad("%s: modality must be one of %v", where, Modalities)
			}
			if s.Mode != "auto" && s.Mode != "combo" {
				bad("%s: mode must be auto or combo", where)
			}
			if s.Mode == "combo" && len(s.Models) == 0 {
				bad("%s: combo needs models", where)
			}
			if s.Strategy != "round_robin" && s.Strategy != "priority" {
				bad("%s: strategy must be round_robin or priority", where)
			}
			w := s.Weights
			if w.Cost < 0 || w.Benchmark < 0 || w.JEV < 0 || w.Reliability < 0 || w.Context < 0 {
				bad("%s: weights must be >= 0", where)
			}
			if s.Mode == "auto" && w.Cost+w.Benchmark+w.JEV+w.Reliability+w.Context <= 0 {
				bad("%s: at least one weight must be > 0", where)
			}
			if s.MinQuality < 0 || s.MinQuality > 1 {
				bad("%s: min_quality must be 0..1", where)
			}
		}
	}
	if c.Kenari.AccountKeyEnv != "" && !envRe.MatchString(c.Kenari.AccountKeyEnv) {
		bad("kenari.account_key_env must look like KENARI_ACCOUNT_KEY")
	}
	if c.Rules.CanaryPercent < 0 || c.Rules.CanaryPercent > 100 {
		bad("rules.canary_percent must be 0..100")
	}
	for _, m := range c.JEV.Judges {
		if strings.TrimSpace(m) == "" {
			bad("jev.judges must not contain an empty model id")
			break
		}
	}
	seenJudge := map[string]bool{}
	for _, m := range c.JEV.Judges {
		if seenJudge[m] {
			bad("jev.judges: %q is listed twice", m)
			break
		}
		seenJudge[m] = true
	}
	for m, v := range c.Benchmarks {
		if v < 0 || v > 100 {
			bad("benchmarks[%q] must be 0..100", m)
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func checkGlobs(errs *[]string, where string, globs []string) {
	for _, g := range globs {
		if _, err := path.Match(g, "x"); err != nil {
			*errs = append(*errs, fmt.Sprintf("%s: bad glob %q", where, g))
		}
	}
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

// DeleteSecret removes one secret from the secrets file.
func DeleteSecret(file, name string) error {
	m := LoadSecrets(file)
	if _, ok := m[name]; !ok {
		return nil
	}
	delete(m, name)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + "\n")
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
