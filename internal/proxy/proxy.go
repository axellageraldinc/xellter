// Package proxy is the public data plane: it authenticates clients, asks the
// router for attempts, forwards requests to kenari (streaming without
// buffering), retries on retriable failures, and records usage.
package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/effort"
	"github.com/axellageraldinc/xellter/internal/route"
	"github.com/axellageraldinc/xellter/internal/rtk"
	"github.com/axellageraldinc/xellter/internal/store"
	"github.com/axellageraldinc/xellter/internal/upstream"
)

// Server is the proxy handler.
type Server struct {
	cfg     atomic.Pointer[config.Config]
	cat     *catalog.Catalog
	pool    *upstream.Pool
	rt      *route.Router
	st      *store.Store
	hc      *http.Client
	Logf    func(string, ...any)
	Now     func() time.Time
	clients atomic.Pointer[map[string]*config.Client]

	// efforts is what each model is known to do with a reasoning effort. It is the
	// same registry the router reads, so a measurement taken while serving one
	// request changes where the next one goes (ADR 0026).
	efforts *effort.Refusals
	// scale corrects the token estimate with what each client's upstream reports (ADR 0031).
	scale tokenScale

	smu   sync.Mutex
	spend map[string]*daySpend

	Inflight  atomic.Int64
	Total     atomic.Int64
	Failed    atomic.Int64
	RTKSaved  atomic.Int64
	startedAt time.Time
}

type daySpend struct {
	day string
	v   float64
}

// New builds the proxy.
func New(cfg *config.Config, cat *catalog.Catalog, pool *upstream.Pool, rt *route.Router, st *store.Store) *Server {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0, // enforced per request
	}
	s := &Server{cat: cat, pool: pool, rt: rt, st: st, hc: &http.Client{Transport: tr}, Logf: func(string, ...any) {}, Now: time.Now, spend: map[string]*daySpend{}, startedAt: time.Now()}
	s.efforts = rt.Efforts()
	s.SetConfig(cfg)
	return s
}

// SetHTTPClient replaces the upstream client (tests).
func (s *Server) SetHTTPClient(c *http.Client) { s.hc = c }

// SetConfig swaps the configuration and rebuilds the client index.
func (s *Server) SetConfig(c *config.Config) {
	s.cfg.Store(c)
	m := map[string]*config.Client{}
	for i := range c.Clients {
		cl := &c.Clients[i]
		if cl.KeyHash != "" {
			m[cl.KeyHash] = cl
		}
	}
	s.clients.Store(&m)
}

// HashKey returns the stored form of a client key.
func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func (s *Server) client(r *http.Request) (*config.Client, int) {
	tok := ""
	if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		tok = strings.TrimSpace(a[7:])
	} else if k := r.Header.Get("x-api-key"); k != "" {
		tok = k
	}
	if tok == "" {
		return nil, 401
	}
	cl := (*s.clients.Load())[HashKey(tok)]
	if cl == nil {
		return nil, 401
	}
	if !cl.Enabled {
		return nil, 403
	}
	return cl, 0
}

// ServeHTTP handles /v1/* and /healthz.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		snap := s.cat.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		if snap == nil {
			w.WriteHeader(503)
			io.WriteString(w, `{"ok":false,"reason":"catalog not loaded"}`)
			return
		}
		dropped, lost := s.st.LogLoss()
		fmt.Fprintf(w, `{"ok":true,"models":%d,"catalog_age_s":%d,"degraded":%t,"log_dropped":%d,"log_lost":%d}`, len(snap.Models), int(time.Since(snap.Fetched).Seconds()), snap.Degraded, dropped, lost)
	case strings.HasPrefix(r.URL.Path, "/v1/"):
		s.api(w, r)
	default:
		writeErr(w, r.URL.Path, 404, "not_found", "unknown path", 0)
	}
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	cl, bad := s.client(r)
	if bad != 0 {
		msg := "invalid or missing API key"
		if bad == 403 {
			msg = "client is disabled"
		}
		writeErr(w, r.URL.Path, bad, "invalid_api_key", msg, 0)
		return
	}
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && (p == "/v1/models" || strings.HasPrefix(p, "/v1/models/")):
		s.listModels(w, r, cl, p)
	case r.Method == http.MethodGet && p == "/v1/usage":
		s.usage(w, r, cl)
	case r.Method == http.MethodGet && p == "/v1/usage/keys":
		s.usageKeys(w, r, cl)
	case r.Method == http.MethodGet && p == "/v1/usage/models":
		s.usageModels(w, r, cl)
	case r.Method == http.MethodGet && p == "/v1/session":
		s.session(w, r, cl)
	case r.Method == http.MethodGet && p == "/v1/account/balance":
		s.balance(w, r)
	case r.Method == http.MethodGet && p == "/v1/account/quota":
		s.quota(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/videos/"):
		s.job(w, r, cl, p)
	case r.Method == http.MethodPost && p == "/v1/x/search":
		s.search(w, r, cl)
	case r.Method == http.MethodPost:
		s.call(w, r, cl)
	default:
		writeErr(w, r.URL.Path, 405, "method_not_allowed", "unsupported method or path", 0)
	}
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request, cl *config.Client, p string) {
	presets, real := s.rt.Models(cl)
	now := s.Now()
	// The list answers in the OpenAI shape, plus the fields a client needs to pick a model sensibly. Without
	// context_length a client falls back to its own default, which is how a preset looked like a 1M-token model.
	type item struct {
		ID            string `json:"id"`
		Object        string `json:"object"`
		Created       int64  `json:"created"`
		OwnedBy       string `json:"owned_by"`
		Modality      string `json:"modality,omitempty"`
		Description   string `json:"description,omitempty"`
		Preset        bool   `json:"preset,omitempty"`
		ContextLength int    `json:"context_length,omitempty"`
		ToolCall      bool   `json:"tool_call,omitempty"`
		Vision        bool   `json:"vision,omitempty"`
		Reasoning     bool   `json:"reasoning,omitempty"`
		// EffortLevels are the reasoning efforts this model can be asked for. They
		// are what kenari publishes, so a client can offer the right choices
		// (ADR 0026).
		EffortLevels []string `json:"effort_levels,omitempty"`
		Pricing      *pricing `json:"pricing,omitempty"`
	}
	var data []item
	for _, n := range presets {
		sh := cl.Presets[n]
		// The pool is the owner's list plus the free and promo models added on its own (ADR 0022): that is
		// what can answer, so that is what the preset advertises.
		pool := s.rt.Pool(cl.Name, n)
		if len(pool) == 0 {
			pool = sh.Models
		}
		desc := sh.Optimization + " over " + fmt.Sprint(len(pool)) + " models"
		it := item{ID: n, Object: "model", Created: s.startedAt.Unix(), OwnedBy: "xellter", Modality: sh.Category,
			Description: desc, Preset: true}
		// A preset advertises what its pool can do: the largest context any model in it has, and whether
		// every model in it supports a feature.
		//
		// The largest, not the smallest (ADR 0026). A preset is a name that may resolve to any model in it, so
		// the window it publishes is a promise about the conversation the client may keep. Publishing the
		// smallest window made clients compress their history early, for a window the model actually serving
		// them never had. A conversation that outgrows the served model's real window is answered with a
		// context-length error, which the client handles by compressing: the right outcome, for the right reason.
		if snap := s.cat.Snapshot(); snap != nil {
			first := true
			for _, id := range pool {
				m := snap.Models[id]
				if m == nil {
					continue
				}
				it.ContextLength = max(it.ContextLength, m.Context)
				if first {
					it.ToolCall, it.Vision, it.Reasoning = m.ToolCall, m.Vision, m.Reasoning
					first = false
					continue
				}
				it.ToolCall = it.ToolCall && m.ToolCall
				it.Vision = it.Vision && m.Vision
				it.Reasoning = it.Reasoning && m.Reasoning
			}
		}
		data = append(data, it)
	}
	snap := s.cat.Snapshot()
	for _, id := range real {
		it := item{ID: id, Object: "model", Created: s.startedAt.Unix(), OwnedBy: "kenari"}
		if snap != nil && snap.Models[id] != nil {
			m := snap.Models[id]
			it.Modality, it.ContextLength = m.Endpoint, m.Context
			it.ToolCall, it.Vision, it.Reasoning = m.ToolCall, m.Vision, m.Reasoning
			it.EffortLevels = m.EffortLevels
			it.Pricing = priceOf(m, now)
		}
		data = append(data, it)
	}
	w.Header().Set("Content-Type", "application/json")
	if id := strings.TrimPrefix(p, "/v1/models/"); id != p {
		for _, it := range data {
			if it.ID == id {
				_ = json.NewEncoder(w).Encode(it)
				return
			}
		}
		writeErr(w, p, 404, "model_not_found", "unknown model", 0)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// ---- request body handling ----

// pricing is what a model costs, in the shape a client reads: Rupiah per 1M
// tokens, in the micro-Rupiah unit the rest of the API uses. A model that is
// free has no price; a model that is priced per unit (an image, a second of
// audio) carries its lines instead of token prices.
type pricing struct {
	Currency   string   `json:"currency"`
	Unit       string   `json:"unit"`
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	Lines      []line   `json:"lines,omitempty"`
}

type line struct {
	Unit    string  `json:"unit"`
	Variant string  `json:"variant,omitempty"`
	Micro   float64 `json:"micro_idr"`
}

// micro turns a Rupiah price into the micro-Rupiah the API publishes.
func micro(idr float64) *float64 {
	v := idr * 1e6
	return &v
}

// priceOf is the catalogue price of a model right now: the token prices for a
// chat-like model, the unit lines for a media model. It is nil when the
// catalogue publishes no price at all.
func priceOf(m *catalog.Model, now time.Time) *pricing {
	if m == nil {
		return nil
	}
	if m.Endpoint == "chat" || m.Endpoint == "systemone" {
		// A chat model is priced per token: input, output and the cache read. The
		// unit lines of the catalogue do not say which is which, so the three
		// fields are used instead. The catalogue holds Rupiah per 1M tokens; the
		// API publishes micro-Rupiah, so the numbers are scaled.
		in, out, cached := m.Effective(now, 0)
		p := &pricing{Currency: "IDR", Unit: "micro_idr_per_1m_tokens"}
		if m.Free || (in == 0 && out == 0) {
			return p
		}
		p.Input, p.Output, p.CacheRead = micro(in), micro(out), micro(cached)
		return p
	}
	// Media is priced per unit: an image, a second of audio, a song.
	if len(m.Lines) > 0 {
		out := &pricing{Currency: "IDR", Unit: m.Lines[0].Unit}
		for _, l := range m.Lines {
			out.Lines = append(out.Lines, line{Unit: l.Unit, Variant: l.Variant, Micro: l.IDR * 1e6})
		}
		return out
	}
	return nil
}

type reqBody struct {
	raw       []byte
	json      map[string]json.RawMessage
	multipart bool
	boundary  string
	ctype     string
	model     string
	est       int // estimated prompt tokens, from estimateTokens; 0 until asked
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) (*reqBody, *route.Error) {
	max := int64(s.cfg.Load().Server.MaxBodyMB) << 20
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		return nil, &route.Error{Status: 413, Code: "body_too_large", Message: "request body too large or unreadable"}
	}
	b := &reqBody{raw: raw, ctype: r.Header.Get("Content-Type")}
	if strings.HasPrefix(b.ctype, "multipart/form-data") {
		b.multipart = true
		b.boundary = boundaryOf(b.ctype)
		b.model = multipartModel(raw, b.boundary)
		return b, nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, &route.Error{Status: 400, Code: "bad_request", Message: "empty body"}
	}
	if err := json.Unmarshal(raw, &b.json); err != nil {
		return nil, &route.Error{Status: 400, Code: "bad_request", Message: "body must be a JSON object"}
	}
	_ = json.Unmarshal(b.json["model"], &b.model)
	return b, nil
}

// encode builds the upstream body for the chosen real model.
func (b *reqBody) encode(model string, streamUsage bool) ([]byte, error) {
	return b.encodeWith(model, streamUsage, nil)
}

// encodeWith builds the upstream body for the chosen real model, with the fields in
// drop removed first.
//
// drop names the top-level fields that must not reach this upstream. A model that
// answers HTTP 400 to the reasoning field is not asked for it again (ADR 0026): the
// request is served without it, which is better than failing the call.
func (b *reqBody) encodeWith(model string, streamUsage bool, drop map[string]bool) ([]byte, error) {
	if b.multipart {
		return rewriteMultipart(b.raw, b.boundary, model)
	}
	m := make(map[string]json.RawMessage, len(b.json)+1)
	for k, v := range b.json {
		if drop[k] {
			continue
		}
		m[k] = v
	}
	m["model"], _ = json.Marshal(model)
	if streamUsage {
		if _, ok := m["stream_options"]; !ok {
			m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
		}
	}
	return json.Marshal(m)
}

// effortFields returns the top-level fields that carry the reasoning effort, so they
// can be removed in one step. The nested "reasoning" object is removed as a whole: a
// model that rejects the effort rejects the object too.
func effortFields() map[string]bool {
	return map[string]bool{"reasoning_effort": true, "reasoning": true}
}

// effortOf reads the reasoning effort a request asks for.
//
// The body is parsed again here rather than kept as a map of any: the request body is
// read once and kept as raw JSON, so decoding the two effort shapes costs one small
// parse of a body that is already in memory.
func (s *Server) effortOf(b *reqBody) (effort.Level, bool) {
	if b == nil || b.json == nil {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal(b.raw, &m); err != nil {
		return "", false
	}
	return effort.Parse(m)
}

// sessionKey identifies a session: an explicit header wins, then the request body's own keys, then a hash of
// the first user message (or, failing that, the opening bytes). The returned value is the session id as the caller would recognise it, because it is
// also what GET /v1/session accepts; the prefix that says where it came from is added separately.
func sessionKey(r *http.Request, b *reqBody) string {
	for _, h := range []string{"X-Xellter-Session", "X-Session-Id", "X-Claude-Code-Session-Id"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	if b == nil || b.json == nil {
		return ""
	}
	for _, k := range []string{"prompt_cache_key", "user"} {
		var v string
		if json.Unmarshal(b.json[k], &v) == nil && v != "" {
			return v
		}
	}
	raw := b.json["messages"]
	if len(raw) == 0 {
		return ""
	}
	// A client that names no session is told apart by how its conversation opens: the first user message,
	// which every later turn repeats. The system prompt is not used: every conversation of one agent starts
	// with the same one, so they would all share a single pin and a single model (ADR 0030).
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &msgs) == nil {
		for _, m := range msgs {
			if m.Role == "user" && len(m.Content) > 0 {
				h := sha256.Sum256(m.Content[:min(len(m.Content), 2000)])
				return "u" + hex.EncodeToString(h[:8])
			}
		}
	}
	h := sha256.Sum256(raw[:min(len(raw), 600)])
	return "m" + hex.EncodeToString(h[:8])
}

func (s *Server) call(w http.ResponseWriter, r *http.Request, cl *config.Client) {
	start := s.Now()
	s.Total.Add(1)
	s.Inflight.Add(1)
	defer s.Inflight.Add(-1)
	cfg := s.cfg.Load()

	body, rerr := s.readBody(w, r)
	if rerr != nil {
		writeErr(w, r.URL.Path, rerr.Status, rerr.Code, rerr.Message, 0)
		return
	}
	session := sessionKey(r, body)
	// The effort the client asked for, and whether it asked at all. A request that says
	// nothing is not a request for "none": an unset effort lets the upstream apply its
	// own default, which for a hosted reasoning model is often its ceiling (ADR 0026).
	asked, hasEffort := s.effortOf(body)
	plan, perr := s.rt.PlanEffort(cl, body.model, session, r.URL.Path, start, s.estimate(cl.Name, body))
	if perr != nil {
		writeErr(w, r.URL.Path, perr.Status, perr.Code, perr.Message, perr.RetryAfter)
		return
	}

	// RTK is always on and has no switch (ADR 0017). It only touches chat tool outputs.
	rtkSaved := 0
	if plan.Modality == "chat" && body.json != nil {
		rtkSaved = rtk.Process(body.json).Saved
		s.RTKSaved.Add(int64(rtkSaved))
	}
	var isStream bool
	if body.json != nil {
		_ = json.Unmarshal(body.json["stream"], &isStream)
	}
	streamUsage := isStream && strings.HasPrefix(r.URL.Path, "/v1/chat/completions")

	snap := s.cat.Snapshot()
	var last *failure
	attempts := 0
	// The plan lists attempts in ring order, one per (model, key). A model is retried once on a retriable
	// failure and then abandoned, so its remaining keys are skipped: the retry is a property of the model, not
	// of the key (requirement 15).
	dead := map[string]bool{}
	ftLimit := s.firstTokenLimit(cfg, s.estimate(cl.Name, body), plan.Modality, isStream)
	for ai, at := range plan.Attempts {
		if dead[at.Model.ID] {
			continue
		}
		// The last model that can still be tried is never timed out: something has to answer.
		timed := ftLimit > 0
		if timed {
			timed = false
			for _, later := range plan.Attempts[ai+1:] {
				if !dead[later.Model.ID] && later.Model.ID != at.Model.ID {
					timed = true
					break
				}
			}
		}
		// A model that has been measured to reject the effort does not get it again
		// (ADR 0026). The request is served without the field, which is better than
		// failing the call over a parameter the client cannot drop.
		//
		// A refusal is exempt from the trust window: it is a property of the API's
		// parameter validation rather than a passing mood, so it holds however old it is.
		drop := map[string]bool(nil)
		if hasEffort && s.efforts.Refuses(at.Model.ID, asked) {
			drop = effortFields()
		}
		for try := 1; try <= 2; try++ {
			// The gate is checked once per (model, key): the retry of a model that just failed must not be
			// refused by the very block that failure created, or requirement 15 could never fire. The key was
			// already validated and counted for this request.
			if try == 1 {
				if why := at.Key.Reserve(at.Model, snap, s.Now()); why != upstream.OK {
					break // this key cannot take it; the model's next key is the next attempt
				}
			}
			attempts++
			sent := at.Model.ID
			if at.Model.IsLocal() {
				sent = at.Model.UpstreamModel
			}
			payload, err := body.encodeWith(sent, streamUsage, drop)
			if err != nil {
				writeErr(w, r.URL.Path, 400, "bad_request", "cannot encode request: "+err.Error(), 0)
				return
			}
			res := s.send(r, at, payload, body.ctype, isStream, cfg)
			row := store.Request{TS: start.UnixMilli(), Client: cl.Name, Preset: plan.Preset, Requested: plan.Requested, Model: at.Model.ID, Upstream: at.Key.Name,
				Path: r.URL.Path, Stream: isStream, Attempt: attempts, RTKSaved: rtkSaved, Session: session, Key: cl.KeyHint}
			// A streamed answer that sends nothing for too long is a model queueing, and the caller is better
			// served by the next one (ADR 0029). Headers alone do not count: some upstreams send them at once.
			if timed && res.err == nil && res.resp.StatusCode < 300 {
				var got bool
				if res, got = s.firstToken(res, ftLimit); !got {
					if r.Context().Err() != nil {
						return
					}
					waited := s.Now().Sub(res.started)
					row.Status, row.Err = 0, fmt.Sprintf("no first token in %ds", int(ftLimit/time.Second))
					row.LatencyMS = int(s.Now().Sub(start) / time.Millisecond)
					row.Billing = "payg"
					s.st.Log(row)
					s.Logf("attempt %d %s/%s on %s: no first token in %s, trying the next model", attempts, plan.Requested, at.Model.ID, at.Key.Name, ftLimit)
					s.observe(cl.Name, plan, session, at.Model.ID, waited, false)
					dead[at.Model.ID] = true
					break
				}
			}
			if res.err == nil && res.resp.StatusCode < 300 {
				s.rt.Succeeded(cl.Name, plan.Preset, at.Model.ID)
				if plan.Preset != "" {
					s.rt.Pin(cl.Name, plan.Preset, session, at.Model.ID, s.Now())
				}
				s.relay(w, r, cl, plan, at, res, row, body, isStream, start, snap)
				return
			}
			f := s.fail(at, res, cfg)
			// A model that answers HTTP 400 to the reasoning field is refusing the
			// parameter, not the request (ADR 0026). The request is served again without
			// the field: the call succeeds instead of dying over a parameter the client
			// cannot drop.
			//
			// The refusal is only recorded when the retry WITHOUT the field succeeds.
			// A 400 can mean several things (a context that does not fit, a body the
			// provider dislikes), and only the retry tells them apart.
			if f.status == 400 && hasEffort && drop == nil && !f.contextOverflow {
				s.Logf("%s refused a request that carried reasoning effort %q; retrying without it", at.Model.ID, asked)
				dead[at.Model.ID] = true
				// The same model, once more, with the effort removed.
				payload, err = body.encodeWith(at.Model.ID, streamUsage, effortFields())
				if err != nil {
					writeErr(w, r.URL.Path, 400, "bad_request", "cannot encode request: "+err.Error(), 0)
					return
				}
				res = s.send(r, at, payload, body.ctype, isStream, cfg)
				if res.err == nil && res.resp.StatusCode < 300 {
					// The field was the problem: the model is known to reject it, and
					// the request is served.
					s.efforts.Record(at.Model.ID, asked, s.Now())
					s.rt.Succeeded(cl.Name, plan.Preset, at.Model.ID)
					if plan.Preset != "" {
						s.rt.Pin(cl.Name, plan.Preset, session, at.Model.ID, s.Now())
					}
					s.relay(w, r, cl, plan, at, res, row, body, isStream, start, snap)
					return
				}
				f = s.fail(at, res, cfg)
			}
			row.Status, row.Err = f.status, f.reason
			row.LatencyMS = int(s.Now().Sub(start) / time.Millisecond)
			row.Billing = "payg"
			s.st.Log(row)
			last = f
			s.Logf("attempt %d %s/%s on %s failed (try %d): status=%d %s", attempts, plan.Requested, at.Model.ID, at.Key.Name, try, f.status, f.reason)
			// A request the caller cancelled is not a failure of the model: the client
			// went away (a closed tab, a stopped stream). Counting it would evict
			// healthy models after three interruptions.
			if r.Context().Err() != nil {
				return
			}
			if f.status == 0 || f.status >= 500 {
				s.observe(cl.Name, plan, session, at.Model.ID, 0, false)
			}
			if plan.Preset != "" && s.rt.FailedWhy(cl.Name, plan.Preset, at.Model.ID, f.reason, s.Now()) {
				// Requirement 16: three errors inside the window take the model out of this preset only, and a
				// session pinned to it moves on (eviction beats stickiness).
				s.rt.Forget(cl.Name, plan.Preset, session)
				s.Logf("%s/%s evicted from preset %s", cl.Name, at.Model.ID, plan.Preset)
			}
			if !f.retry {
				dead[at.Model.ID] = true
				break
			}
			if try == 2 {
				dead[at.Model.ID] = true
			}
		}
	}
	s.Failed.Add(1)
	if last != nil && last.body != nil { // relay the upstream's own error
		for k, v := range last.header {
			w.Header()[k] = v
		}
		w.Header().Set("X-Xellter-Attempts", strconv.Itoa(attempts))
		w.WriteHeader(last.status)
		_, _ = w.Write(last.body)
		return
	}
	msg := "all upstream attempts failed"
	if last != nil {
		msg += ": " + last.reason
	} else {
		msg = "no upstream could take the request (rate limits or cooldowns)"
	}
	writeErr(w, r.URL.Path, 503, "upstream_unavailable", msg, 15*time.Second)
}

type sendResult struct {
	resp    *http.Response
	err     error
	cancel  context.CancelFunc
	started time.Time
	ttfb    time.Duration
	// timedOut is set when the first-byte timer, not the caller, cancelled the request.
	timedOut *atomic.Bool
}

type failure struct {
	status int
	reason string
	retry  bool
	body   []byte
	header http.Header
	// contextOverflow says the upstream refused because the request did not fit the
	// model's window. It is the caller's history, not a fault of the model, so it is
	// neither retried on another key nor counted against the model (ADR 0026).
	contextOverflow bool
}

func (s *Server) send(r *http.Request, at route.Attempt, payload []byte, ctype string, stream bool, cfg *config.Config) sendResult {
	ctx, cancel := context.WithCancel(r.Context())
	limit := time.Duration(cfg.Kenari.FirstByteSecs) * time.Second
	switch {
	case at.Model.Endpoint == "music":
		limit = time.Duration(cfg.Kenari.MusicTimeoutSec) * time.Second
	case !stream:
		limit *= 3
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(limit, func() { timedOut.Store(true); cancel() })
	base := cfg.Kenari.BaseURL
	if at.Model.IsLocal() {
		base = at.Model.BaseURL
	}
	url := base + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	copyHeaders(req.Header, r.Header)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if !at.Model.IsLocal() { // a local server gets no kenari key (ADR 0033)
		req.Header.Set("Authorization", "Bearer "+at.Key.Secret())
		req.Header.Set("x-api-key", at.Key.Secret())
	}
	req.Header.Set("User-Agent", cfg.Kenari.UserAgent)
	t0 := s.Now()
	resp, err := s.hc.Do(req)
	timer.Stop()
	return sendResult{resp: resp, err: err, cancel: cancel, started: t0, ttfb: s.Now().Sub(t0), timedOut: &timedOut}
}

var forwardHeaders = []string{"Accept", "Anthropic-Version", "Anthropic-Beta", "Openai-Beta", "Openai-Organization"}

func copyHeaders(dst, src http.Header) {
	for _, h := range forwardHeaders {
		if v := src.Values(h); len(v) > 0 {
			dst[http.CanonicalHeaderKey(h)] = append([]string(nil), v...)
		}
	}
}

func (s *Server) fail(at route.Attempt, res sendResult, cfg *config.Config) *failure {
	defer res.cancel()
	now := s.Now()
	if res.err != nil {
		// A request the caller cancelled is not the key's failure and not the
		// model's: the client went away. The key keeps its state.
		if errors.Is(res.err, context.Canceled) {
			if res.timedOut != nil && res.timedOut.Load() {
				// The upstream did not answer in time. That is the model's slowness, not the key's, and the same
				// model will not do better a second later: the next one is tried at once.
				return &failure{status: 0, reason: fmt.Sprintf("no response within %ds", int(now.Sub(res.started)/time.Second)), retry: false}
			}
			return &failure{status: 0, reason: "cancelled by the client", retry: false}
		}
		v := at.Key.Failure(at.Model.ID, 0, nil, 0, now)
		return &failure{status: 0, reason: "network: " + trimErr(res.err), retry: v.Retry}
	}
	defer res.resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.resp.Body, 256<<10))
	ra := parseRetryAfter(res.resp.Header.Get("Retry-After"))
	v := at.Key.Failure(at.Model.ID, res.resp.StatusCode, body, ra, now)
	h := http.Header{}
	for _, k := range []string{"Content-Type", "Retry-After"} {
		if x := res.resp.Header.Get(k); x != "" {
			h.Set(k, x)
		}
	}
	return &failure{status: res.resp.StatusCode, reason: v.Reason, retry: v.Retry, body: body, header: h,
		contextOverflow: v.Reason == "context length exceeded"}
}

func trimErr(err error) string {
	s := err.Error()
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

func parseRetryAfter(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// relay streams a successful upstream response to the client and records it.
func (s *Server) relay(w http.ResponseWriter, r *http.Request, cl *config.Client, plan *route.Plan, at route.Attempt, res sendResult, row store.Request, body *reqBody, stream bool, start time.Time, snap *catalog.Snapshot) {
	defer res.cancel()
	defer res.resp.Body.Close()
	resp := res.resp
	for _, k := range []string{"Content-Type", "Cache-Control", "X-Request-Id", "Openai-Processing-Ms"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("X-Xellter-Model", at.Model.ID)
	w.Header().Set("X-Xellter-Attempt", strconv.Itoa(row.Attempt))
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	head := make([]byte, 0, 4096)
	tail := newTail(16 << 10)
	buf := make([]byte, 16<<10)
	var ttft time.Duration
	total := 0
	var copyErr error
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if ttft == 0 {
				ttft = s.Now().Sub(start)
			}
			if len(head) < cap(head) {
				head = append(head, buf[:min(n, cap(head)-len(head))]...)
			}
			tail.write(buf[:n])
			total += n
			if _, werr := w.Write(buf[:n]); werr != nil {
				copyErr = werr
				break
			}
			_ = rc.Flush()
		}
		if err != nil {
			if err != io.EOF {
				copyErr = err
			}
			break
		}
	}
	now := s.Now()
	row.Status = resp.StatusCode
	row.LatencyMS = int(now.Sub(start) / time.Millisecond)
	row.TTFTMS = int(ttft / time.Millisecond)
	row.OK = copyErr == nil
	if copyErr != nil {
		row.Err = "stream: " + trimErr(copyErr)
	}
	u := findUsage(head, tail.bytes())
	row.InTok, row.OutTok, row.CachedTok = u.in, u.out, u.cached
	if body != nil && !body.multipart && body.est > 0 {
		s.scale.learn(row.Client, body.est, u.in)
	}
	if copyErr == nil {
		var waited time.Duration
		if stream && plan.Modality == "chat" && ttft > 0 {
			waited = ttft - res.started.Sub(start)
		}
		s.observe(row.Client, plan, row.Session, at.Model.ID, waited, true)
	}
	covered := at.Key.Covered(at.Model, snap, now)
	switch {
	case at.Model.Free:
		row.Billing = "free"
	case covered:
		row.Billing = "plan"
	default:
		row.Billing = "payg"
	}
	row.CatalogCost = s.cost(cl, at, plan, body, u, covered, now, snap)
	s.st.Log(row)
	if row.OK {
		at.Key.Success(at.Model.ID, now)
		if row.Billing == "plan" {
			at.Key.RecordSpend(now, row.CatalogCost)
		}
		s.addSpend(cl.Name, row.CatalogCost)
		if strings.HasPrefix(r.URL.Path, "/v1/videos") && total <= 16<<10 { // the whole job JSON is in the tail buffer
			var j struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(bytes.TrimSpace(tail.bytes()), &j) == nil && j.ID != "" {
				s.st.PutJob(j.ID, at.Key.Name)
			}
		}
	} else {
		s.Failed.Add(1)
	}
}

func (s *Server) cost(cl *config.Client, at route.Attempt, plan *route.Plan, b *reqBody, u usage, covered bool, now time.Time, snap *catalog.Snapshot) float64 {
	m := at.Model
	if m.Free {
		return 0
	}
	if m.Endpoint == "chat" || m.Endpoint == "systemone" {
		return usageCost(m, u.in, u.out, u.cached, at.Key.FreeCache(m, snap, now), now)
	}
	// Media is priced per unit, and the request says how many units it asked for.
	var n float64
	var str string
	if b.json != nil {
		switch m.Endpoint {
		case "images":
			if json.Unmarshal(b.json["n"], &n) != nil || n <= 0 {
				n = 1
			}
		case "audio_speech":
			if json.Unmarshal(b.json["input"], &str) == nil {
				n = float64(len([]rune(str)))
			}
		case "videos":
			if json.Unmarshal(b.json["duration"], &n) != nil || n <= 0 {
				n = 5
			}
			if json.Unmarshal(b.json["resolution"], &str) == nil {
				return mediaCost(m, "second", str, n)
			}
		case "audio_transcription":
			n = 60
		case "music":
			n = 1
		}
	}
	return mediaCost(m, unitOf(m.Endpoint), "", n)
}

// usageCost is the catalog price of a finished chat request.
func usageCost(m *catalog.Model, inTok, outTok, cachedTok int, freeCache bool, now time.Time) float64 {
	if m.Free {
		return 0
	}
	in, out, cached := m.Effective(now, 0)
	if cachedTok > inTok {
		cachedTok = inTok
	}
	cp := cached
	if freeCache {
		cp = 0
	}
	return (float64(inTok-cachedTok)*in + float64(cachedTok)*cp + float64(outTok)*out) / 1e6
}

// unitOf is the catalog price unit a media category is billed in.
func unitOf(category string) string {
	switch category {
	case "images":
		return "image"
	case "videos":
		return "second"
	case "audio_speech":
		return "1k_chars"
	case "audio_transcription":
		return "second"
	case "music":
		return "song"
	}
	return ""
}

// mediaCost prices n units of a media model at catalog rates (the cheapest line of the unit, or the named
// variant when the request asked for one).
func mediaCost(m *catalog.Model, unit, variant string, n float64) float64 {
	var best *catalog.Line
	for i := range m.Lines {
		l := &m.Lines[i]
		if l.Unit != unit {
			continue
		}
		if variant != "" && l.Variant == variant {
			return l.IDR * n
		}
		if best == nil || l.IDR < best.IDR {
			best = l
		}
	}
	if best == nil {
		return 0
	}
	if unit == "1k_chars" {
		return best.IDR * n / 1000
	}
	return best.IDR * n
}

// ---- video jobs ----

func (s *Server) job(w http.ResponseWriter, r *http.Request, cl *config.Client, p string) {
	id := strings.TrimPrefix(p, "/v1/videos/")
	if i := strings.Index(id, "/"); i >= 0 {
		id = id[:i]
	}
	name, ok := s.st.Job(id)
	key := s.pool.Get(name)
	if !ok || key == nil {
		writeErr(w, p, 404, "job_not_found", "unknown video job (xellter only knows jobs it created)", 0)
		return
	}
	cfg := s.cfg.Load()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Kenari.BaseURL+r.URL.Path, nil)
	req.Header.Set("Authorization", "Bearer "+key.Secret())
	req.Header.Set("User-Agent", cfg.Kenari.UserAgent)
	resp, err := s.hc.Do(req)
	if err != nil {
		writeErr(w, p, 502, "upstream_unavailable", trimErr(err), 5*time.Second)
		return
	}
	defer resp.Body.Close()
	if v := resp.Header.Get("Content-Type"); v != "" {
		w.Header().Set("Content-Type", v)
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

// ---- client budget ----

func (s *Server) spentToday(client string) float64 {
	day := s.Now().Format("2006-01-02")
	s.smu.Lock()
	defer s.smu.Unlock()
	d := s.spend[client]
	if d == nil || d.day != day {
		v := 0.0
		if s.st != nil {
			t := s.Now()
			midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			v, _ = s.st.ClientSpend(client, midnight)
		}
		d = &daySpend{day: day, v: v}
		s.spend[client] = d
	}
	return d.v
}

func (s *Server) addSpend(client string, v float64) {
	if v <= 0 {
		return
	}
	s.spentToday(client) // ensure the day entry exists
	s.smu.Lock()
	s.spend[client].v += v
	s.smu.Unlock()
}

// SpentToday exposes the counter for the dashboard.
func (s *Server) SpentToday(client string) float64 { return s.spentToday(client) }

// ---- errors ----

func writeErr(w http.ResponseWriter, path string, status int, code, msg string, retry time.Duration) {
	w.Header().Set("Content-Type", "application/json")
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
	}
	w.WriteHeader(status)
	if strings.HasPrefix(path, "/v1/messages") {
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": code, "message": msg}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "xellter_error", "code": code}})
}

// observe tells the router how one request of a model went (ADR 0029) and logs the verdict when it
// takes the model out of the preset or moves a session off it.
func (s *Server) observe(client string, plan *route.Plan, session, model string, waited time.Duration, ok bool) {
	if plan.Preset == "" {
		return
	}
	why, moved := s.rt.Observe(client, plan.Preset, session, model, waited, ok, s.Now())
	if why != "" {
		s.Logf("%s/%s out of preset %s: %s", client, model, plan.Preset, why)
	}
	if moved {
		s.Logf("%s: session %.12s leaves %s, which keeps answering slowly", client, session, model)
	}
}

// firstTokenLimit is how long a streamed chat answer may stay silent before the next model is tried. It
// grows with the size of the request, because a long prompt legitimately takes longer to read.
func (s *Server) firstTokenLimit(cfg *config.Config, promptTokens int, modality string, stream bool) time.Duration {
	secs := cfg.Routing.FirstTokenSecs
	if secs < 0 || !stream || modality != "chat" {
		return 0
	}
	if secs == 0 {
		secs = 15
	}
	d := time.Duration(secs+promptTokens/10000) * time.Second
	return min(d, 60*time.Second)
}

// firstToken waits for the first bytes of a streamed answer. It reports false when nothing arrived within
// limit, after cancelling the request. Bytes that did arrive are put back in front of the body.
func (s *Server) firstToken(res sendResult, limit time.Duration) (sendResult, bool) {
	type chunk struct {
		b   []byte
		err error
	}
	ch := make(chan chunk, 1)
	buf := make([]byte, 16<<10)
	body := res.resp.Body
	go func() {
		n, err := body.Read(buf)
		ch <- chunk{buf[:n], err}
	}()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case c := <-ch:
		res.resp.Body = &replayBody{first: c.b, err: c.err, rest: body}
		return res, true
	case <-t.C:
		res.cancel() // unblocks the read
		<-ch
		_ = body.Close()
		return res, false
	}
}

// replayBody gives back the bytes that were read ahead, then the rest of the body.
type replayBody struct {
	first []byte
	err   error // what the read-ahead ended with, returned once the bytes are consumed
	rest  io.ReadCloser
}

func (b *replayBody) Read(p []byte) (int, error) {
	if len(b.first) > 0 {
		n := copy(p, b.first)
		b.first = b.first[n:]
		return n, nil
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.rest.Read(p)
}

func (b *replayBody) Close() error { return b.rest.Close() }
