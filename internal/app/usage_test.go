package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// usageSummary mirrors the shape kenari answers on /v1/usage.
type usageSummary struct {
	Object string `json:"object"`
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Data   []struct {
		BillingKind  string `json:"billing_kind"`
		Cached       int64  `json:"cached_prompt_tokens"`
		CatalogMicro int64  `json:"catalog_micro_idr"`
		Completion   int64  `json:"completion_tokens"`
		CostMicro    int64  `json:"cost_micro_idr"`
		Model        string `json:"model"`
		Prompt       int64  `json:"prompt_tokens"`
		Requests     int    `json:"requests"`
	} `json:"data"`
	Total struct {
		Requests     int   `json:"requests"`
		Prompt       int64 `json:"prompt_tokens"`
		Completion   int64 `json:"completion_tokens"`
		Cached       int64 `json:"cached_prompt_tokens"`
		CostMicro    int64 `json:"cost_micro_idr"`
		CatalogMicro int64 `json:"catalog_micro_idr"`
	} `json:"total"`
}

func (e *env) get(t *testing.T, key, path string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.px.URL+path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp, string(b)
}

func TestUsageEndpointListsTheRealModelsPerClient(t *testing.T) {
	e := setup(t, nil)
	// the fixture's fake returns 1000 prompt and 50 completion tokens
	if r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text")); r.StatusCode != 200 {
		t.Fatalf("chat: %d", r.StatusCode)
	}
	e.a.St.Flush()

	resp, body := e.get(t, e.keyCl, "/v1/usage?range=24h")
	if resp.StatusCode != 200 {
		t.Fatalf("usage: %d %s", resp.StatusCode, body)
	}
	var u usageSummary
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatalf("usage is not json: %v %s", err, body)
	}
	if u.Object != "usage.summary" {
		t.Fatalf("object: %q", u.Object)
	}
	if len(u.Data) != 1 {
		t.Fatalf("one model row expected, got %d: %s", len(u.Data), body)
	}
	row := u.Data[0]
	// the real model behind the "text" preset, not the preset name
	if row.Model == "text" || row.Model == "" {
		t.Fatalf("the row must name the real model: %+v", row)
	}
	if row.Requests != 1 || row.Prompt != 1000 || row.Completion != 50 {
		t.Fatalf("row wrong: %+v", row)
	}
	if row.BillingKind != "free" {
		t.Fatalf("billing: %q", row.BillingKind)
	}
	if row.CatalogMicro != 0 || row.CostMicro != 0 {
		t.Fatalf("a free model costs nothing: %+v", row)
	}
	if u.Total.Requests != 1 || u.Total.Prompt != 1000 || u.Total.Completion != 50 {
		t.Fatalf("totals wrong: %+v", u.Total)
	}
	if u.Total.Requests != sumRequests(u.Data) {
		t.Fatalf("the total must be the sum of the rows: %+v", u.Total)
	}
}

func sumRequests(rows []struct {
	BillingKind  string `json:"billing_kind"`
	Cached       int64  `json:"cached_prompt_tokens"`
	CatalogMicro int64  `json:"catalog_micro_idr"`
	Completion   int64  `json:"completion_tokens"`
	CostMicro    int64  `json:"cost_micro_idr"`
	Model        string `json:"model"`
	Prompt       int64  `json:"prompt_tokens"`
	Requests     int    `json:"requests"`
}) int {
	n := 0
	for _, r := range rows {
		n += r.Requests
	}
	return n
}

func TestUsageEndpointIsScopedToTheClientAndNeedsAKey(t *testing.T) {
	e := setup(t, nil)
	if r, _ := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text")); r.StatusCode != 200 {
		t.Fatalf("chat: %d", r.StatusCode)
	}
	e.a.St.Flush()
	if r, _ := e.get(t, "", "/v1/usage"); r.StatusCode != 401 {
		t.Fatalf("no key: %d", r.StatusCode)
	}
	if r, _ := e.get(t, "xl-wrong", "/v1/usage"); r.StatusCode != 401 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}
	// another client sees no rows of the first one
	_, body := e.get(t, "xl-direct", "/v1/usage?range=24h")
	var u usageSummary
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatalf("usage is not json: %v %s", err, body)
	}
	if len(u.Data) != 0 {
		t.Fatalf("another client must not see the traffic: %s", body)
	}
}

func TestUsageEndpointRejectsAnUnknownRange(t *testing.T) {
	e := setup(t, nil)
	if r, _ := e.get(t, e.keyCl, "/v1/usage?range=7d"); r.StatusCode != 400 {
		t.Fatalf("bad range: %d", r.StatusCode)
	}
}

func TestAccountEndpointsAnswerInKenariShape(t *testing.T) {
	e := setup(t, nil)
	reset := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	e.f.account = func(auth string) (int, string) {
		return 200, `{"plan":{"name":"Indie","windows":{"week":{"budget_rp":1000,"used_rp":990,"remaining_rp":10,"used_frac":0.99,"resets_at":"` + reset + `"}}},"coupon":null,"alert_pct":80}`
	}
	e.a.ProbeKeys(context.Background())
	resp, body := e.get(t, e.keyCl, "/v1/account/balance")
	if resp.StatusCode != 200 {
		t.Fatalf("balance: %d %s", resp.StatusCode, body)
	}
	var bal map[string]any
	if err := json.Unmarshal([]byte(body), &bal); err != nil {
		t.Fatalf("balance is not json: %v %s", err, body)
	}
	for _, k := range []string{"available_micro_idr", "balance_micro_idr", "currency", "object"} {
		if _, ok := bal[k]; !ok {
			t.Fatalf("balance is missing %q: %s", k, body)
		}
	}
	if bal["currency"] != "IDR" {
		t.Fatalf("currency: %v", bal["currency"])
	}
	if bal["available_micro_idr"].(float64) != 24998500000 {
		t.Fatalf("available: %v", bal["available_micro_idr"])
	}
	resp, body = e.get(t, e.keyCl, "/v1/account/quota")
	if resp.StatusCode != 200 {
		t.Fatalf("quota: %d %s", resp.StatusCode, body)
	}
	var q struct {
		Plan struct {
			Name    string `json:"name"`
			Windows map[string]struct {
				BudgetRP float64 `json:"budget_rp"`
				UsedRP   float64 `json:"used_rp"`
				UsedFrac float64 `json:"used_frac"`
			} `json:"windows"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("quota is not json: %v %s", err, body)
	}
	if q.Plan.Name != "Indie" {
		t.Fatalf("plan name: %q", q.Plan.Name)
	}
	w := q.Plan.Windows["week"]
	if w.BudgetRP != 1000 || w.UsedRP != 990 || w.UsedFrac != 0.99 {
		t.Fatalf("week window: %+v", w)
	}
}

// Requirement 12: /v1/session says which real model the session is using, and 404s when it has no traffic yet.
func TestSessionEndpointNamesThePinnedModel(t *testing.T) {
	e := setup(t, nil)
	// nothing has happened in this session yet
	if r, b := e.get(t, e.keyCl, "/v1/session?session=s-none"); r.StatusCode != 404 {
		t.Fatalf("an unknown session must be 404: %d %s", r.StatusCode, b)
	}
	if r, _ := e.get(t, e.keyCl, "/v1/session"); r.StatusCode != 400 {
		t.Fatalf("no session at all must be 400: %d", r.StatusCode)
	}
	if r, _ := e.get(t, "", "/v1/session?session=s1"); r.StatusCode != 401 {
		t.Fatalf("no key: %d", r.StatusCode)
	}

	resp, body := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text"), "X-Xellter-Session", "s1")
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	served := resp.Header.Get("X-Xellter-Model")
	e.a.St.Flush()

	// the caller reads its session back with the same header it sends with requests
	resp, body = e.get(t, e.keyCl, "/v1/session", "X-Xellter-Session", "s1")
	if resp.StatusCode != 200 {
		t.Fatalf("session: %d %s", resp.StatusCode, body)
	}
	var s struct {
		Object   string   `json:"object"`
		Session  string   `json:"session"`
		Client   string   `json:"client"`
		Preset   string   `json:"preset"`
		Model    string   `json:"model"`
		Billing  string   `json:"billing"`
		Context  int      `json:"context_window"`
		ToolCall bool     `json:"tool_call"`
		PinnedAt int64    `json:"pinned_at"`
		Requests int      `json:"requests"`
		InTok    int64    `json:"in_tok"`
		OutTok   int64    `json:"out_tok"`
		Models   []string `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("session is not json: %v %s", err, body)
	}
	if s.Object != "session" || s.Client != "babu" || s.Preset != "text" {
		t.Fatalf("session header: %+v", s)
	}
	if s.Model != served || s.Model == "text" || s.Model == "" {
		t.Fatalf("the session must name the real model (%q): %+v", served, s)
	}
	if s.Billing == "" || s.Context == 0 || !s.ToolCall || s.PinnedAt == 0 {
		t.Fatalf("the model's numbers are missing: %+v", s)
	}
	if s.Requests != 1 || s.InTok != 1000 || s.OutTok != 50 || len(s.Models) != 1 || s.Models[0] != served {
		t.Fatalf("session traffic: %+v", s)
	}

	// the id the session was logged under is what the endpoint reports, and the query parameter accepts it
	resp, body = e.get(t, e.keyCl, "/v1/session?session="+s.Session)
	if resp.StatusCode != 200 || !strings.Contains(body, `"model":"`+served+`"`) {
		t.Fatalf("session by query: %d %s", resp.StatusCode, body)
	}
	// a different session header is a different session
	if r, _ := e.get(t, e.keyCl, "/v1/session", "X-Xellter-Session", "s2"); r.StatusCode != 404 {
		t.Fatalf("another session has no traffic: %d", r.StatusCode)
	}
	// and another client cannot read this session
	if r, _ := e.get(t, "xl-direct", "/v1/session?session="+s.Session); r.StatusCode != 404 {
		t.Fatalf("a session belongs to one client: %d", r.StatusCode)
	}
}

// Requirement 18: usage splits by the xellter key that served the request and drills down to preset and model.
func TestUsageByKeyAndByModel(t *testing.T) {
	e := setup(t, nil)
	for range 2 {
		if r, b := e.post(t, e.keyCl, "/v1/chat/completions", fmt.Sprintf(chatBody, "text")); r.StatusCode != 200 {
			t.Fatalf("chat: %d %s", r.StatusCode, b)
		}
	}
	e.a.St.Flush()

	resp, body := e.get(t, e.keyCl, "/v1/usage/keys?range=24h")
	if resp.StatusCode != 200 {
		t.Fatalf("usage/keys: %d %s", resp.StatusCode, body)
	}
	var keys struct {
		Object string `json:"object"`
		Data   []struct {
			APIKey   string  `json:"api_key"`
			Client   string  `json:"client"`
			Requests int     `json:"requests"`
			InTok    int64   `json:"in_tok"`
			OutTok   int64   `json:"out_tok"`
			CostIDR  float64 `json:"cost_idr"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &keys); err != nil {
		t.Fatalf("usage/keys is not json: %v %s", err, body)
	}
	if keys.Object != "usage.keys" || len(keys.Data) == 0 {
		t.Fatalf("usage/keys: %s", body)
	}
	// The row is the xellter API key the caller used, identified by its hint: that is what the owner hands out
	// and revokes. The key itself must never appear.
	total := 0
	for _, k := range keys.Data {
		if k.Client != "babu" || k.APIKey == "" || k.APIKey == e.keyCl {
			t.Fatalf("a key row names the xellter key by its hint, never the key: %+v", k)
		}
		total += k.Requests
	}
	if total != 2 {
		t.Fatalf("both requests must be counted, got %d: %s", total, body)
	}

	resp, body = e.get(t, e.keyCl, "/v1/usage/models?range=24h&preset=text")
	if resp.StatusCode != 200 {
		t.Fatalf("usage/models: %d %s", resp.StatusCode, body)
	}
	var models struct {
		Object string `json:"object"`
		Data   []struct {
			Preset   string `json:"preset"`
			Model    string `json:"model"`
			Requests int    `json:"requests"`
			InTok    int64  `json:"in_tok"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &models); err != nil {
		t.Fatalf("usage/models is not json: %v %s", err, body)
	}
	if models.Object != "usage.models" || len(models.Data) == 0 {
		t.Fatalf("usage/models: %s", body)
	}
	for _, m := range models.Data {
		if m.Preset != "text" || m.Model == "" || m.Model == "text" || m.InTok == 0 {
			t.Fatalf("a model row must name the real model and its preset: %+v", m)
		}
	}
	// the wrong window is refused on both endpoints
	if r, _ := e.get(t, e.keyCl, "/v1/usage/keys?range=7d"); r.StatusCode != 400 {
		t.Fatalf("usage/keys bad range: %d", r.StatusCode)
	}
	if r, _ := e.get(t, e.keyCl, "/v1/usage/models?range=7d"); r.StatusCode != 400 {
		t.Fatalf("usage/models bad range: %d", r.StatusCode)
	}
	// another client sees nothing
	_, body = e.get(t, "xl-direct", "/v1/usage/keys?range=24h")
	if !strings.Contains(body, `"data":[]`) {
		t.Fatalf("another client must not see the traffic: %s", body)
	}
}

// A session is pinned when its model starts answering, but its request is logged when the answer ends. A
// caller that asks in between (a chat showing the model name during the first reply) must get the pin.
func TestSessionEndpointNamesTheModelBeforeTheFirstReplyIsLogged(t *testing.T) {
	e := setup(t, nil)
	e.a.Rt.Pin("babu", "text", "s-first", "free-a:free", time.Now())
	resp, body := e.get(t, e.keyCl, "/v1/session?session=s-first")
	if resp.StatusCode != 200 {
		t.Fatalf("a pinned session with no log yet must answer: %d %s", resp.StatusCode, body)
	}
	var s struct {
		Model  string `json:"model"`
		Preset string `json:"preset"`
	}
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	if s.Model != "free-a:free" || s.Preset != "text" {
		t.Fatalf("got model %q preset %q", s.Model, s.Preset)
	}
}
