// Package catalog mirrors the kenari public catalog (models, live prices,
// promos, plans, free-tier limits) into an immutable in-memory snapshot.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const micro = 1e6

// Promo is a time-limited discount; prices on Model already include it.
type Promo struct {
	PercentOff float64   `json:"percent_off"`
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
}

// Upcoming is a scheduled price change.
type Upcoming struct {
	EffectiveAt time.Time `json:"effective_at"`
	InIDR       float64   `json:"in_idr"`
	OutIDR      float64   `json:"out_idr"`
}

// Line is a per-unit price (image, second, 1k chars, song).
type Line struct {
	Unit    string  `json:"unit"`
	Variant string  `json:"variant"`
	IDR     float64 `json:"idr"`
}

// Model is one catalog entry with prices in IDR.
type Model struct {
	ID        string   `json:"id"`
	Owner     string   `json:"owner"`
	Endpoint  string   `json:"endpoint"` // chat | images | videos | audio_speech | audio_transcription | music | other
	Endpoints []string `json:"endpoints"`
	Context   int      `json:"context"`
	Free      bool     `json:"free"`
	ToolCall  bool     `json:"tool_call"`
	Reasoning bool     `json:"reasoning"`
	// EffortLevels are the reasoning efforts kenari publishes for this model (its
	// `supported_reasoning_levels`). Empty means kenari publishes nothing, which is
	// NOT the same as "no levels": an empty list says nothing about the model, and
	// measured upstreams accept efforts kenari does not list (ADR 0026).
	EffortLevels []string `json:"effort_levels,omitempty"`
	// EffortToggle says the model has a reasoning switch that can be turned off
	// (kenari's `reasoning_toggle`).
	EffortToggle bool       `json:"effort_toggle,omitempty"`
	Vision       bool       `json:"vision"`
	Protocols    []string   `json:"protocols"`
	InIDR        float64    `json:"in_idr"` // IDR per 1M tokens
	OutIDR       float64    `json:"out_idr"`
	CachedIDR    float64    `json:"cached_idr"`      // cache read
	CacheWrite   float64    `json:"cache_write_idr"` // 0 = kenari publishes no value; shown as — in the panel
	Lines        []Line     `json:"lines"`
	Promo        *Promo     `json:"promo,omitempty"`
	Upcoming     *Upcoming  `json:"upcoming,omitempty"`
	SunsetAt     *time.Time `json:"sunset_at,omitempty"`
	Subscriber   bool       `json:"subscriber_only"`
	FreeSibling  string     `json:"-"` // id of the paid twin of a ":free" model
	// BaseURL is set on a model served from this machine (ADR 0033): requests go there, with no kenari
	// key. UpstreamModel is the name that server knows the model by.
	BaseURL       string `json:"base_url,omitempty"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	// Provider is the upstream the model belongs to. Empty means the built-in one (config.DefaultProvider), which
	// is also what a snapshot saved before providers existed says (ADR 0040).
	Provider string `json:"provider,omitempty"`
}

// DefaultProvider is the provider of a model that names none.
const DefaultProvider = "kenari"

// ProviderID is the provider the model belongs to.
func (m *Model) ProviderID() string {
	if m == nil || m.Provider == "" {
		return DefaultProvider
	}
	return m.Provider
}

// WireID is the model name the upstream knows. A model of an extra provider is listed as "<provider>/<id>" here
// and sent as "<id>"; a local model is sent under the name its server knows.
func (m *Model) WireID() string {
	if m.UpstreamModel != "" {
		return m.UpstreamModel
	}
	return m.ID
}

// IsLocal reports whether the model runs on this machine instead of at kenari.
func (m *Model) IsLocal() bool { return m != nil && m.BaseURL != "" }

// Local describes a model served from this machine.
type Local struct {
	ID, Category, BaseURL, Model string
}

// WithLocal returns a source that adds the local models to what src fetches. A model whose id kenari
// already uses is left alone, so a local entry can never shadow a kenari model. locals is read on every
// fetch, so a reloaded configuration takes effect at the next refresh.
func WithLocal(src Source, locals func() []Local) Source { return localSource{src, locals} }

type localSource struct {
	Source
	locals func() []Local
}

func (l localSource) Fetch(ctx context.Context) (*Snapshot, error) {
	s, err := l.Source.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	for _, lm := range l.locals() {
		if _, taken := s.Models[lm.ID]; taken {
			continue
		}
		up := lm.Model
		if up == "" {
			up = lm.ID
		}
		s.Models[lm.ID] = &Model{ID: lm.ID, Owner: "local", Endpoint: lm.Category, Endpoints: []string{lm.Category},
			Free: true, BaseURL: strings.TrimRight(lm.BaseURL, "/"), UpstreamModel: up}
	}
	return s, nil
}

// Plan is a kenari subscription.
type Plan struct {
	Name       string   `json:"name"`
	PriceIDR   float64  `json:"price_idr"`
	Budget5h   float64  `json:"budget_5h_idr"` // 0 = no ceiling
	BudgetWeek float64  `json:"budget_week_idr"`
	BudgetMon  float64  `json:"budget_month_idr"`
	Scope      []string `json:"scope_models"`
	FreeCache  []string `json:"free_cache_models"`
}

// TierLimit is a free-tier style limit.
type TierLimit struct {
	RPM   int `json:"rpm"`
	Daily int `json:"daily"` // 0 = unlimited
}

// FreeTier maps tier name to limits.
type FreeTier struct {
	Free   TierLimit `json:"free"`
	Topup  TierLimit `json:"topup"`
	Plan   TierLimit `json:"plan"`
	Thresh float64   `json:"threshold_idr"`
}

// Snapshot is one immutable view of the catalog.
type Snapshot struct {
	Models   map[string]*Model `json:"models"`
	Plans    []Plan            `json:"plans"`
	Tier     FreeTier          `json:"free_tier"`
	USDIDR   float64           `json:"usd_idr"`
	Fetched  time.Time         `json:"fetched"`
	Degraded bool              `json:"-"`
}

// Plan returns the named plan (case-insensitive).
func (s *Snapshot) Plan(name string) *Plan {
	for i := range s.Plans {
		if strings.EqualFold(s.Plans[i].Name, name) {
			return &s.Plans[i]
		}
	}
	return nil
}

// IDs returns sorted model ids.
func (s *Snapshot) IDs() []string {
	ids := make([]string, 0, len(s.Models))
	for id := range s.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Source fetches the catalog; it is an interface so tests can use a fake.
type Source interface {
	Fetch(ctx context.Context) (*Snapshot, error)
}

// Catalog holds the current snapshot and refreshes it in the background.
type Catalog struct {
	cur   atomic.Pointer[Snapshot]
	src   Source
	cache KV
	onNew func(*Snapshot)
}

// KV is where the catalog keeps its last good snapshot (the store).
type KV interface {
	KVGet(k string) string
	KVSet(k, v string)
}

const cacheKey = "catalog.snapshot"

// New creates a catalog; cache (optional) persists the last good snapshot.
func New(src Source, cache KV) *Catalog {
	c := &Catalog{src: src, cache: cache}
	if cache != nil {
		if b := cache.KVGet(cacheKey); b != "" {
			var s Snapshot
			if json.Unmarshal([]byte(b), &s) == nil && len(s.Models) > 0 {
				s.Degraded = true
				c.cur.Store(&s)
			}
		}
	}
	return c
}

// Snapshot returns the current snapshot (never nil after a successful start).
func (c *Catalog) Snapshot() *Snapshot { return c.cur.Load() }

// OnUpdate registers a callback run after each successful refresh.
func (c *Catalog) OnUpdate(f func(*Snapshot)) { c.onNew = f }

// Refresh fetches once and swaps the snapshot.
func (c *Catalog) Refresh(ctx context.Context) error {
	s, err := c.src.Fetch(ctx)
	if err != nil {
		if old := c.cur.Load(); old != nil {
			cp := *old
			cp.Degraded = true
			c.cur.Store(&cp)
		}
		return err
	}
	c.cur.Store(s)
	if c.cache != nil {
		if b, err := json.Marshal(s); err == nil {
			c.cache.KVSet(cacheKey, string(b))
		}
	}
	if c.onNew != nil {
		c.onNew(s)
	}
	return nil
}

// Run refreshes immediately and then every interval until ctx ends.
func (c *Catalog) Run(ctx context.Context, interval func() time.Duration, logf func(string, ...any)) {
	for {
		if err := c.Refresh(ctx); err != nil {
			logf("catalog refresh failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval()):
		}
	}
}

// ---- kenari HTTP source ----

// Kenari is the live source.
type Kenari struct {
	BaseURL string
	UA      string
	HTTP    *http.Client
}

func (k *Kenari) get(ctx context.Context, p string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, k.BaseURL+p, nil)
	req.Header.Set("User-Agent", k.UA) // the default Go UA is blocked with 403
	req.Header.Set("Accept", "application/json")
	resp, err := k.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", p, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

type rawModel struct {
	ID            string   `json:"id"`
	OwnedBy       string   `json:"owned_by"`
	Endpoints     []string `json:"endpoints"`
	ContextLength int      `json:"context_length"`
	ToolCall      bool     `json:"tool_call"`
	Reasoning     bool     `json:"reasoning"`
	// ReasoningToggle says the model can have its reasoning switched off.
	ReasoningToggle bool `json:"reasoning_toggle"`
	// SupportedReasoningLevels are the efforts kenari publishes for the model.
	SupportedReasoningLevels []string `json:"supported_reasoning_levels"`
	Protocols                []string `json:"protocols"`
	SunsetAt                 *int64   `json:"sunset_at"`
	Modalities               struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Pricing struct {
		Free       bool     `json:"free"`
		Input      *float64 `json:"input"`
		Output     *float64 `json:"output"`
		CacheRead  *float64 `json:"cache_read"`
		CacheWrite *float64 `json:"cache_write"`
	} `json:"pricing"`
	Lines []struct {
		Unit    string  `json:"unit"`
		Variant *string `json:"variant"`
		Micro   float64 `json:"micro_idr"`
	} `json:"pricing_lines"`
}

type rawPromo struct {
	PercentOff float64 `json:"percent_off"`
	StartsAt   int64   `json:"starts_at"`
	EndsAt     int64   `json:"ends_at"`
}

type rawItem struct {
	Model         string          `json:"model"`
	Subscriber    bool            `json:"subscriber_only"`
	Promo         *rawPromo       `json:"promo"`
	UpcomingPrice json.RawMessage `json:"upcoming_price"`
	SunsetAt      *int64          `json:"sunset_at"`
	ImageMicro    *float64        `json:"image_micro_idr_per_image"`
}

// Fetch implements Source.
func (k *Kenari) Fetch(ctx context.Context) (*Snapshot, error) {
	var models struct {
		Data []rawModel `json:"data"`
	}
	if err := k.get(ctx, "/v1/models", &models); err != nil {
		return nil, err
	}
	if len(models.Data) == 0 {
		return nil, errors.New("empty model list")
	}
	s := &Snapshot{Models: map[string]*Model{}, Fetched: time.Now(), USDIDR: 17500}
	for _, r := range models.Data {
		m := &Model{ID: r.ID, Owner: r.OwnedBy, Endpoints: r.Endpoints, Context: r.ContextLength, Free: r.Pricing.Free || strings.HasSuffix(r.ID, ":free"),
			ToolCall: r.ToolCall, Reasoning: r.Reasoning, Protocols: r.Protocols,
			EffortToggle: r.ReasoningToggle, EffortLevels: r.SupportedReasoningLevels}
		m.Endpoint = "other"
		if len(r.Endpoints) > 0 {
			m.Endpoint = r.Endpoints[0]
		}
		for _, in := range r.Modalities.Input {
			if in == "image" {
				m.Vision = true
			}
		}
		if r.Pricing.Input != nil {
			m.InIDR = *r.Pricing.Input / micro
		}
		if r.Pricing.Output != nil {
			m.OutIDR = *r.Pricing.Output / micro
		}
		if r.Pricing.CacheRead != nil {
			m.CachedIDR = *r.Pricing.CacheRead / micro
		} else {
			m.CachedIDR = m.InIDR
		}
		if r.Pricing.CacheWrite != nil {
			m.CacheWrite = *r.Pricing.CacheWrite / micro
		}
		for _, l := range r.Lines {
			v := ""
			if l.Variant != nil {
				v = *l.Variant
			}
			m.Lines = append(m.Lines, Line{Unit: l.Unit, Variant: v, IDR: l.Micro / micro})
		}
		if r.SunsetAt != nil && *r.SunsetAt > 0 {
			t := time.Unix(*r.SunsetAt, 0)
			m.SunsetAt = &t
		}
		if strings.HasSuffix(r.ID, ":free") {
			m.FreeSibling = strings.TrimSuffix(r.ID, ":free")
		}
		s.Models[m.ID] = m
	}

	// System One models live on their own catalog endpoint and are a preset category of their own (the sixth).
	// They are not chat models: nothing here mixes them into the chat pool.
	var sys struct {
		Data []rawModel `json:"data"`
	}
	if err := k.get(ctx, "/v1/models?modality=systemone", &sys); err == nil {
		for _, r := range sys.Data {
			if _, taken := s.Models[r.ID]; taken {
				continue
			}
			m := &Model{ID: r.ID, Owner: r.OwnedBy, Endpoint: "systemone", Endpoints: []string{"systemone"},
				Free: r.Pricing.Free || strings.HasSuffix(r.ID, ":free"), Context: r.ContextLength}
			if r.Pricing.Input != nil {
				m.InIDR = *r.Pricing.Input / micro
			}
			if r.Pricing.Output != nil {
				m.OutIDR = *r.Pricing.Output / micro
			}
			if r.Pricing.CacheRead != nil {
				m.CachedIDR = *r.Pricing.CacheRead / micro
			} else {
				m.CachedIDR = m.InIDR
			}
			if r.Pricing.CacheWrite != nil {
				m.CacheWrite = *r.Pricing.CacheWrite / micro
			}
			if strings.HasSuffix(r.ID, ":free") {
				m.FreeSibling = strings.TrimSuffix(r.ID, ":free")
			}
			s.Models[m.ID] = m
		}
	}

	// Public pricing: promos, upcoming prices, subscriber-only flags, free tier.
	var pricing struct {
		FreeTier struct {
			Daily int `json:"daily"`
			RPM   int `json:"rpm"`
			Next  struct {
				Daily int `json:"daily"`
				RPM   int `json:"rpm"`
			} `json:"next"`
			Plan struct {
				Daily int `json:"daily"`
				RPM   int `json:"rpm"`
			} `json:"plan"`
			Threshold float64 `json:"threshold_idr"`
		} `json:"free_tier"`
		USDIDR float64   `json:"usd_idr_rate"`
		Items  []rawItem `json:"items"`
	}
	if err := k.get(ctx, "/api/public/pricing", &pricing); err == nil {
		s.Tier = FreeTier{
			Free: TierLimit{pricing.FreeTier.RPM, pricing.FreeTier.Daily}, Topup: TierLimit{pricing.FreeTier.Next.RPM, pricing.FreeTier.Next.Daily},
			Plan: TierLimit{pricing.FreeTier.Plan.RPM, pricing.FreeTier.Plan.Daily}, Thresh: pricing.FreeTier.Threshold,
		}
		if pricing.USDIDR > 0 {
			s.USDIDR = pricing.USDIDR
		}
		for _, it := range pricing.Items {
			m := s.Models[it.Model]
			if m == nil {
				continue
			}
			m.Subscriber = it.Subscriber
			if it.Promo != nil && it.Promo.EndsAt > 0 {
				m.Promo = &Promo{PercentOff: it.Promo.PercentOff, StartsAt: time.Unix(it.Promo.StartsAt, 0), EndsAt: time.Unix(it.Promo.EndsAt, 0)}
			}
			if m.SunsetAt == nil && it.SunsetAt != nil && *it.SunsetAt > 0 {
				t := time.Unix(*it.SunsetAt, 0)
				m.SunsetAt = &t
			}
			if m.Endpoint == "images" && len(m.Lines) == 0 && it.ImageMicro != nil {
				m.Lines = append(m.Lines, Line{Unit: "image", IDR: *it.ImageMicro / micro})
			}
			if u := parseUpcoming(it.UpcomingPrice); u != nil {
				m.Upcoming = u
			}
		}
	}
	if s.Tier.Free.RPM == 0 {
		s.Tier = FreeTier{Free: TierLimit{5, 50}, Topup: TierLimit{10, 1000}, Plan: TierLimit{25, 0}, Thresh: 10000}
	}

	var plans []struct {
		Name      string   `json:"name"`
		Price     float64  `json:"price_idr"`
		B5h       float64  `json:"budget_5h_micro_idr"`
		BWeek     float64  `json:"budget_week_micro_idr"`
		BMonth    float64  `json:"budget_month_micro_idr"`
		Scope     []string `json:"scope_models"`
		FreeCache []string `json:"free_cache_models"`
	}
	var plansResp json.RawMessage
	if err := k.get(ctx, "/api/plans", &plansResp); err == nil {
		if json.Unmarshal(plansResp, &plans) != nil {
			var wrapped struct {
				Data []json.RawMessage `json:"data"`
			}
			if json.Unmarshal(plansResp, &wrapped) == nil {
				b, _ := json.Marshal(wrapped.Data)
				_ = json.Unmarshal(b, &plans)
			}
		}
		for _, p := range plans {
			s.Plans = append(s.Plans, Plan{Name: p.Name, PriceIDR: p.Price, Budget5h: p.B5h / micro, BudgetWeek: p.BWeek / micro, BudgetMon: p.BMonth / micro, Scope: p.Scope, FreeCache: p.FreeCache})
		}
	}

	return s, nil
}

func parseUpcoming(raw json.RawMessage) *Upcoming {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	num := func(keys ...string) float64 {
		for _, k := range keys {
			if v, ok := m[k].(float64); ok {
				return v
			}
		}
		return 0
	}
	u := &Upcoming{InIDR: num("price_in_micro_idr_per_1m", "input") / micro, OutIDR: num("price_out_micro_idr_per_1m", "output") / micro}
	if t := num("effective_at", "starts_at", "at"); t > 0 {
		u.EffectiveAt = time.Unix(int64(t), 0)
	}
	if u.EffectiveAt.IsZero() {
		return nil
	}
	return u
}

// Effective returns the price the model will have at time t, honouring promo
// expiry and scheduled changes. The listed price already includes a running promo.
func (m *Model) Effective(t time.Time, lookahead time.Duration) (in, out, cached float64) {
	in, out, cached = m.InIDR, m.OutIDR, m.CachedIDR
	if m.Promo != nil && !m.Promo.EndsAt.IsZero() && t.After(m.Promo.EndsAt) && m.Promo.PercentOff > 0 && m.Promo.PercentOff < 100 {
		f := 100 / (100 - m.Promo.PercentOff) // promo ended: back to list price
		in, out, cached = in*f, out*f, cached*f
	}
	if u := m.Upcoming; u != nil && !u.EffectiveAt.After(t.Add(lookahead)) {
		in, out = u.InIDR, u.OutIDR
		if m.InIDR > 0 {
			cached = cached * (u.InIDR / m.InIDR)
		}
	}
	return
}

// PromoActive reports a running promo with at least minRemaining left.
func (m *Model) PromoActive(t time.Time, minRemaining time.Duration) bool {
	p := m.Promo
	return p != nil && p.PercentOff > 0 && !t.Before(p.StartsAt) && p.EndsAt.Sub(t) >= minRemaining
}

// Undiscounted returns the token prices without a running promo.
func (m *Model) Undiscounted() (in, out float64) {
	in, out = m.InIDR, m.OutIDR
	if p := m.Promo; p != nil && p.PercentOff > 0 && p.PercentOff < 100 {
		f := 100 / (100 - p.PercentOff)
		in, out = in*f, out*f
	}
	return
}
