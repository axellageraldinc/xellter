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
	Upstreams  []Upstream         `yaml:"upstreams" json:"upstreams"`
	Clients    []Client           `yaml:"clients" json:"clients"`
	Presets    map[string]Preset  `yaml:"presets" json:"presets"`
	OldShadows map[string]Preset  `yaml:"shadows,omitempty" json:"-"` // pre-rename name, migrated on load
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
}

// Upstream is one kenari API key. Tier drives rate limits; Plan names the
// subscription (for plan-covered models) when Tier is "plan".
type Upstream struct {
	Name    string   `yaml:"name" json:"name"`
	KeyEnv  string   `yaml:"key_env" json:"key_env"`
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Tier    string   `yaml:"tier" json:"tier"` // free | topup | plan
	Plan    string   `yaml:"plan" json:"plan"`
	Account string   `yaml:"account" json:"account"`       // keys of one kenari account share plan spend; default: own account
	RPM     int      `yaml:"rpm" json:"rpm"`               // 0 = tier default
	Daily   int      `yaml:"daily_free" json:"daily_free"` // 0 = tier default, -1 = unlimited
	Allow   []string `yaml:"allow" json:"allow"`           // model globs this key may use; empty = all (learned from 400s)
	Note    string   `yaml:"note" json:"note"`
}

// Client is a consumer of xellter (Babu, aixell, polifin, ...). Each client has
// exactly one API key, stored only as a hash.
type Client struct {
	Name        string   `yaml:"name" json:"name"`
	Enabled     bool     `yaml:"enabled" json:"enabled"`
	KeyHash     string   `yaml:"key_hash" json:"key_hash"`
	KeyHint     string   `yaml:"key_hint" json:"key_hint"`
	Presets     []string `yaml:"presets" json:"presets"`                   // preset models this client may call; "*" = all
	OldShadows  []string `yaml:"shadows,omitempty" json:"-"`               // pre-rename name, migrated on load
	Passthrough []string `yaml:"passthrough" json:"passthrough"`           // real model globs allowed directly; empty = none
	Upstreams   []string `yaml:"upstreams" json:"upstreams"`               // restrict to these upstream names; empty = all
	DailyBudget float64  `yaml:"daily_budget_idr" json:"daily_budget_idr"` // 0 = unlimited
	RTK         *bool    `yaml:"rtk" json:"rtk"`                           // nil = follow global
	Note        string   `yaml:"note" json:"note"`
}

// Weights are relative weights of the ranking components.
type Weights struct {
	Cost        float64 `yaml:"cost" json:"cost"`
	Benchmark   float64 `yaml:"benchmark" json:"benchmark"`
	JEV         float64 `yaml:"jev" json:"jev"`
	Reliability float64 `yaml:"reliability" json:"reliability"`
	Context     float64 `yaml:"context" json:"context"`
}

// Require are hard capability gates.
type Require struct {
	ToolCall   bool `yaml:"tool_call" json:"tool_call"`
	Vision     bool `yaml:"vision" json:"vision"`
	Reasoning  bool `yaml:"reasoning" json:"reasoning"`
	MinContext int  `yaml:"min_context" json:"min_context"`
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
// mode "combo" uses Models as an explicit ordered list.
type Preset struct {
	Description   string      `yaml:"description" json:"description"`
	Modality      string      `yaml:"modality" json:"modality"` // chat | images | videos | audio_speech | audio_transcription | music
	Mode          string      `yaml:"mode" json:"mode"`         // auto | combo
	Models        []string    `yaml:"models" json:"models"`     // combo: ordered list
	Allow         []string    `yaml:"allow" json:"allow"`       // auto: model globs (empty = all)
	Deny          []string    `yaml:"deny" json:"deny"`
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
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	Model          string   `yaml:"model" json:"model"`
	Upstream       string   `yaml:"upstream" json:"upstream"` // upstream name used for evaluation calls
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

// Default returns the built-in configuration (no upstreams, no clients).
func Default() *Config {
	c := &Config{
		Server: Server{
			Listen: "127.0.0.1:8480", DataDir: "data", MaxBodyMB: 64, LogRetention: 30,
			LogRequests: true, TrustedProxy: true, SessionHours: 12,
			AdminHosts: []string{"localhost", "127.0.0.1"},
		},
		Kenari: Kenari{BaseURL: "https://kenari.id", SyncMinutes: 10, UserAgent: "xellter/1.0", FirstByteSecs: 120, MusicTimeoutSec: 900},
		Rules: Rules{
			FreeBonus: 0.10, PlanBonus: 0.08, PlanCostFactor: 0.3, PromoBonusMax: 0.05, PromoMinRemainingHrs: 12, UpcomingLookaheadHrs: 24,
			NewModelDays: 14, CanaryPercent: 5, CanaryMinRequests: 20, CanaryMinSuccess: 0.9, AvoidSunsetDays: 3,
			StickyMinutes: 30, StatsWindowHours: 24,
		},
		Resilience: Resilience{MaxAttempts: 4, CooldownBaseSec: 30, CooldownMaxSec: 900, BreakerFailures: 5, BreakerOpenSec: 120},
		JEV: JEV{
			Enabled: false, Model: "jev-1-13:free", DailyBudgetIDR: 1000, EveryHours: 24, PassThreshold: 0.6,
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

// DefaultPresets is the starter set used when a config has no presets.
func DefaultPresets() map[string]Preset {
	chat := Weights{Cost: 30, Benchmark: 25, JEV: 20, Reliability: 15, Context: 10}
	cp := CostProfile{InputTokens: 4000, OutputTokens: 600, CachedRatio: 0.7}
	media := Weights{Cost: 70, Benchmark: 10, Reliability: 20}
	return map[string]Preset{
		"xl-balanced": {Description: "Everyday chat: good answers at a fair price", Modality: "chat", Mode: "auto", Require: Require{ToolCall: true, MinContext: 32000},
			MinQuality: 0.45, MaxCandidates: 4, Strategy: "round_robin", RRBand: 0.04, Weights: chat, Cost: cp},
		"xl-budget": {Description: "Cheapest acceptable model for simple tasks", Modality: "chat", Mode: "auto", Require: Require{MinContext: 16000},
			MinQuality: 0.30, MaxCandidates: 4, Strategy: "round_robin", RRBand: 0.05, Weights: Weights{Cost: 50, Benchmark: 15, JEV: 10, Reliability: 20, Context: 5}, Cost: cp},
		"xl-smart": {Description: "Best answers; cost matters less", Modality: "chat", Mode: "auto", Require: Require{ToolCall: true, MinContext: 64000},
			MinQuality: 0.6, MaxCandidates: 3, Strategy: "priority", RRBand: 0.03, Weights: Weights{Cost: 10, Benchmark: 35, JEV: 30, Reliability: 15, Context: 10}, Cost: cp},
		"xl-coding": {Description: "Coding agents: tool use, long context, quality first", Modality: "chat", Mode: "auto", Require: Require{ToolCall: true, MinContext: 64000},
			MinQuality: 0.55, MaxCandidates: 4, Strategy: "priority", RRBand: 0.03, Weights: Weights{Cost: 20, Benchmark: 35, JEV: 20, Reliability: 15, Context: 10}, Cost: cp},
		"xl-image":      {Description: "Image generation, cheapest first", Modality: "images", Mode: "auto", MaxCandidates: 4, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{Images: 1}},
		"xl-video":      {Description: "Video generation, cheapest first", Modality: "videos", Mode: "auto", MaxCandidates: 3, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{Seconds: 5, Variant: "720p"}},
		"xl-speech":     {Description: "Text to speech, cheapest first", Modality: "audio_speech", Mode: "auto", MaxCandidates: 3, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{Chars: 1000}},
		"xl-transcribe": {Description: "Speech to text, cheapest first", Modality: "audio_transcription", Mode: "auto", MaxCandidates: 3, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{Seconds: 60}},
		"xl-music":      {Description: "Music generation, cheapest first", Modality: "music", Mode: "auto", MaxCandidates: 2, Strategy: "priority", RRBand: 0.03, Weights: media, Cost: CostProfile{}},
	}
}

// Parse decodes YAML on top of defaults and validates the result.
func Parse(data []byte) (*Config, error) {
	c := Default()
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	c.migrate()
	if len(c.Presets) == 0 {
		c.Presets = DefaultPresets()
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

// migrate moves the pre-rename "shadows" keys into "presets".
func (c *Config) migrate() {
	if len(c.Presets) == 0 && len(c.OldShadows) > 0 {
		c.Presets = c.OldShadows
	}
	c.OldShadows = nil
	for i := range c.Clients {
		if len(c.Clients[i].Presets) == 0 && len(c.Clients[i].OldShadows) > 0 {
			c.Clients[i].Presets = c.Clients[i].OldShadows
		}
		c.Clients[i].OldShadows = nil
	}
}

// Normalize fills defaults and validates a config built outside Parse (for
// example decoded from the admin panel's JSON).
func Normalize(c *Config) error {
	if len(c.Presets) == 0 {
		c.Presets = DefaultPresets()
	}
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
	if c.JEV.Model == "" {
		c.JEV.Model = d.JEV.Model
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
	for i := range c.Upstreams {
		if c.Upstreams[i].Tier == "" {
			c.Upstreams[i].Tier = "free"
		}
	}
	for name, s := range c.Presets {
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
		c.Presets[name] = s
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
	seen := map[string]bool{}
	for i, u := range c.Upstreams {
		if !nameRe.MatchString(u.Name) {
			bad("upstreams[%d].name is invalid", i)
		}
		if seen[u.Name] {
			bad("duplicate upstream %q", u.Name)
		}
		seen[u.Name] = true
		if !envRe.MatchString(u.KeyEnv) {
			bad("upstream %q: key_env must look like KENARI_KEY_X", u.Name)
		}
		if u.Tier != "free" && u.Tier != "topup" && u.Tier != "plan" {
			bad("upstream %q: tier must be free, topup or plan", u.Name)
		}
		checkGlobs(&errs, "upstream "+u.Name+".allow", u.Allow)
	}
	cseen := map[string]bool{}
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
		for _, s := range cl.Presets {
			if _, ok := c.Presets[s]; !ok && s != "*" {
				bad("client %q: unknown preset %q", cl.Name, s)
			}
		}
		for _, un := range cl.Upstreams {
			if !seen[un] {
				bad("client %q: unknown upstream %q", cl.Name, un)
			}
		}
		checkGlobs(&errs, "client "+cl.Name+".passthrough", cl.Passthrough)
	}
	for name, s := range c.Presets {
		if !nameRe.MatchString(name) {
			bad("preset name %q is invalid", name)
		}
		if !contains(Modalities, s.Modality) {
			bad("preset %q: modality must be one of %v", name, Modalities)
		}
		if s.Mode != "auto" && s.Mode != "combo" {
			bad("preset %q: mode must be auto or combo", name)
		}
		if s.Mode == "combo" && len(s.Models) == 0 {
			bad("preset %q: combo needs models", name)
		}
		if s.Strategy != "round_robin" && s.Strategy != "priority" {
			bad("preset %q: strategy must be round_robin or priority", name)
		}
		w := s.Weights
		if w.Cost < 0 || w.Benchmark < 0 || w.JEV < 0 || w.Reliability < 0 || w.Context < 0 {
			bad("preset %q: weights must be >= 0", name)
		}
		if s.Mode == "auto" && w.Cost+w.Benchmark+w.JEV+w.Reliability+w.Context <= 0 {
			bad("preset %q: at least one weight must be > 0", name)
		}
		if s.MinQuality < 0 || s.MinQuality > 1 {
			bad("preset %q: min_quality must be 0..1", name)
		}
		checkGlobs(&errs, "preset "+name+".allow", s.Allow)
		checkGlobs(&errs, "preset "+name+".deny", s.Deny)
	}
	if c.Rules.CanaryPercent < 0 || c.Rules.CanaryPercent > 100 {
		bad("rules.canary_percent must be 0..100")
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
