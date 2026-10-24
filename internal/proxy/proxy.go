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
	"github.com/axellageraldinc/xellter/internal/rank"
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
		fmt.Fprintf(w, `{"ok":true,"models":%d,"catalog_age_s":%d,"degraded":%t}`, len(snap.Models), int(time.Since(snap.Fetched).Seconds()), snap.Degraded)
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
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/videos/"):
		s.job(w, r, cl, p)
	case r.Method == http.MethodPost:
		s.call(w, r, cl)
	default:
		writeErr(w, r.URL.Path, 405, "method_not_allowed", "unsupported method or path", 0)
	}
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request, cl *config.Client, p string) {
	presets, real := s.rt.Models(cl)
	cfg := s.cfg.Load()
	type item struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		Created     int64  `json:"created"`
		OwnedBy     string `json:"owned_by"`
		Modality    string `json:"modality,omitempty"`
		Description string `json:"description,omitempty"`
		Preset      bool   `json:"preset,omitempty"`
	}
	var data []item
	for _, n := range presets {
		sh := cfg.Presets[n]
		data = append(data, item{n, "model", s.startedAt.Unix(), "xellter", sh.Modality, sh.Description, true})
	}
	snap := s.cat.Snapshot()
	for _, id := range real {
		it := item{ID: id, Object: "model", Created: s.startedAt.Unix(), OwnedBy: "kenari"}
		if snap != nil && snap.Models[id] != nil {
			it.Modality = snap.Models[id].Endpoint
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

type reqBody struct {
	raw       []byte
	json      map[string]json.RawMessage
	multipart bool
	boundary  string
	ctype     string
	model     string
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
	if b.multipart {
		return rewriteMultipart(b.raw, b.boundary, model)
	}
	m := make(map[string]json.RawMessage, len(b.json)+1)
	for k, v := range b.json {
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

func sessionKey(r *http.Request, b *reqBody) string {
	for _, h := range []string{"X-Xellter-Session", "X-Session-Id", "X-Claude-Code-Session-Id"} {
		if v := r.Header.Get(h); v != "" {
			return h + v
		}
	}
	if b.json == nil {
		return ""
	}
	for _, k := range []string{"prompt_cache_key", "user"} {
		var v string
		if json.Unmarshal(b.json[k], &v) == nil && v != "" {
			return k + v
		}
	}
	if raw := b.json["messages"]; len(raw) > 0 {
		n := min(len(raw), 600)
		h := sha256.Sum256(raw[:n])
		return "m" + hex.EncodeToString(h[:8])
	}
	return ""
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
	if cl.DailyBudget > 0 && s.spentToday(cl.Name) >= cl.DailyBudget {
		writeErr(w, r.URL.Path, 429, "budget_exceeded", fmt.Sprintf("daily budget of Rp%.0f used", cl.DailyBudget), time.Minute)
		return
	}
	plan, perr := s.rt.Plan(cl, body.model, sessionKey(r, body), r.URL.Path, start)
	if perr != nil {
		writeErr(w, r.URL.Path, perr.Status, perr.Code, perr.Message, perr.RetryAfter)
		return
	}

	rtkSaved := 0
	rtkOn := cfg.RTK.Enabled
	if cl.RTK != nil {
		rtkOn = *cl.RTK
	}
	if rtkOn && plan.Modality == "chat" && body.json != nil && r.Header.Get("X-Xellter-RTK") != "off" {
		rtkSaved = rtk.Process(body.json, cfg.RTK).Saved
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
	for i, at := range plan.Attempts {
		if why := at.Key.Reserve(at.Model, snap, s.Now()); why != upstream.OK {
			continue
		}
		attempts++
		payload, err := body.encode(at.Model.ID, streamUsage)
		if err != nil {
			writeErr(w, r.URL.Path, 400, "bad_request", "cannot encode request: "+err.Error(), 0)
			return
		}
		res := s.send(r, at, payload, body.ctype, isStream, cfg)
		row := store.Request{TS: start.UnixMilli(), Client: cl.Name, Preset: plan.Preset, Requested: plan.Requested, Model: at.Model.ID, Upstream: at.Key.Name,
			Path: r.URL.Path, Stream: isStream, Attempt: i + 1, Canary: at.Canary, RTKSaved: rtkSaved}
		if res.err == nil && res.resp.StatusCode < 300 {
			s.relay(w, r, cl, plan, at, res, row, body, isStream, start, snap)
			return
		}
		f := s.fail(at, res, cfg)
		row.Status, row.Err = f.status, f.reason
		row.LatencyMS = int(s.Now().Sub(start) / time.Millisecond)
		row.Billing = "payg"
		s.st.Log(row)
		last = f
		s.Logf("attempt %d %s/%s on %s failed: status=%d %s", i+1, plan.Requested, at.Model.ID, at.Key.Name, f.status, f.reason)
		if !f.retry {
			break
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
}

type failure struct {
	status int
	reason string
	retry  bool
	body   []byte
	header http.Header
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
	timer := time.AfterFunc(limit, cancel)
	url := cfg.Kenari.BaseURL + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	copyHeaders(req.Header, r.Header)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Authorization", "Bearer "+at.Key.Secret)
	req.Header.Set("x-api-key", at.Key.Secret)
	req.Header.Set("User-Agent", cfg.Kenari.UserAgent)
	t0 := s.Now()
	resp, err := s.hc.Do(req)
	timer.Stop()
	return sendResult{resp: resp, err: err, cancel: cancel, started: t0, ttfb: s.Now().Sub(t0)}
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
		v := at.Key.Failure(at.Model.ID, 0, nil, 0, cfg.Resilience, now)
		return &failure{status: 0, reason: "network: " + trimErr(res.err), retry: v.Retry}
	}
	defer res.resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.resp.Body, 256<<10))
	ra := parseRetryAfter(res.resp.Header.Get("Retry-After"))
	v := at.Key.Failure(at.Model.ID, res.resp.StatusCode, body, ra, cfg.Resilience, now)
	h := http.Header{}
	for _, k := range []string{"Content-Type", "Retry-After"} {
		if x := res.resp.Header.Get(k); x != "" {
			h.Set(k, x)
		}
	}
	return &failure{status: res.resp.StatusCode, reason: v.Reason, retry: v.Retry, body: body, header: h}
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
	covered := at.Key.Covered(at.Model, snap, now)
	switch {
	case at.Model.Free:
		row.Billing = "free"
	case covered:
		row.Billing = "plan"
	default:
		row.Billing = "payg"
	}
	row.CatalogCost = s.cost(at, plan, body, u, covered, now, snap)
	s.st.Log(row)
	if row.OK {
		at.Key.Success(at.Model.ID, now)
		if row.Billing == "plan" {
			at.Key.RecordSpend(now, row.CatalogCost)
		}
		if plan.Preset != "" {
			s.rt.Remember(cl.Name, plan.Preset, sessionKey(r, body), at.Model.ID, at.Key.Name, now)
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

func (s *Server) cost(at route.Attempt, plan *route.Plan, b *reqBody, u usage, covered bool, now time.Time, snap *catalog.Snapshot) float64 {
	m := at.Model
	if m.Free {
		return 0
	}
	if m.Endpoint == "chat" {
		return rank.UsageCost(m, u.in, u.out, u.cached, at.Key.FreeCache(m, snap, now), now)
	}
	prof := config.CostProfile{}
	if sh, ok := s.cfg.Load().Presets[plan.Preset]; ok {
		prof = sh.Cost
	}
	if b.json != nil {
		var n float64
		var str string
		switch m.Endpoint {
		case "images":
			if json.Unmarshal(b.json["n"], &n) == nil && n > 0 {
				prof.Images = n
			}
		case "audio_speech":
			if json.Unmarshal(b.json["input"], &str) == nil {
				prof.Chars = float64(len([]rune(str)))
			}
		case "videos":
			if json.Unmarshal(b.json["duration"], &n) == nil && n > 0 {
				prof.Seconds = n
			}
			if json.Unmarshal(b.json["resolution"], &str) == nil {
				prof.Variant = str
			}
		}
	}
	c, _ := rank.RefCost(m, prof, now, 0)
	return c
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
	req.Header.Set("Authorization", "Bearer "+key.Secret)
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
