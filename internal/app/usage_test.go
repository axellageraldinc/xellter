package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

func (e *env) get(t *testing.T, key, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.px.URL+path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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
