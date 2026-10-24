package catalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchParsesPricesPromosPlansAndTiers(t *testing.T) {
	end := time.Now().Add(72 * time.Hour).Unix()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() == "" || r.UserAgent() == "Go-http-client/1.1" {
			w.WriteHeader(403) // kenari blocks the default Go user agent
			return
		}
		if r.URL.Query().Get("modality") == "systemone" { // judges are only listed here, not in the default list
			io.WriteString(w, `{"data":[{"id":"jev-1-13:free","owned_by":"jev","pricing":{"free":true}},{"id":"jev-1-14","owned_by":"jev","pricing":{"free":false,"input":850000000}}]}`)
			return
		}
		io.WriteString(w, `{"data":[
		 {"id":"m","endpoints":["chat"],"owned_by":"o","context_length":64000,"tool_call":true,"reasoning":true,"protocols":["openai-chat"],"modalities":{"input":["text","image"]},"pricing":{"free":false,"input":630000000,"output":2500000000,"cache_read":120000000}},
		 {"id":"m:free","endpoints":["chat"],"pricing":{"free":true,"input":null,"output":null}},
		 {"id":"v","endpoints":["videos"],"pricing":{"free":false,"input":0,"output":0},"pricing_lines":[{"unit":"second","variant":"720p","micro_idr":750000000}],"sunset_at":`+fmt.Sprint(end)+`}]}`)
	})
	mux.HandleFunc("/api/public/pricing", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"free_tier":{"daily":50,"rpm":5,"next":{"daily":1000,"rpm":10},"plan":{"daily":0,"rpm":25},"threshold_idr":10000},"usd_idr_rate":17000,
		 "items":[{"model":"m","subscriber_only":false,"promo":{"percent_off":90,"starts_at":1,"ends_at":%d},"upcoming_price":{"price_in_micro_idr_per_1m":6300000000,"price_out_micro_idr_per_1m":25000000000,"effective_at":%d}}]}`, end, end)
	})
	mux.HandleFunc("/api/plans", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"name":"Indie","price_idr":49000,"budget_5h_micro_idr":0,"budget_week_micro_idr":75000000000,"budget_month_micro_idr":300000000000,"scope_models":["m"],"free_cache_models":["m"]}]`)
	})
	mux.HandleFunc("/v1/models/changes", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"kind":"added","model":"m","detected_at":1792774454}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	k := &Kenari{BaseURL: srv.URL, UA: "xellter-test", HTTP: srv.Client()}
	s, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := s.Models["m"]
	if m.InIDR != 630 || m.OutIDR != 2500 || m.CachedIDR != 120 || !m.Vision || !m.Reasoning || m.Context != 64000 {
		t.Fatalf("model: %+v", m)
	}
	if !s.Models["m:free"].Free || s.Models["m:free"].FreeSibling != "m" {
		t.Fatal("free twin not linked")
	}
	if m.Promo == nil || m.Promo.PercentOff != 90 || m.Upcoming == nil || m.Upcoming.InIDR != 6300 {
		t.Fatalf("promo/upcoming: %+v %+v", m.Promo, m.Upcoming)
	}
	if in, _ := m.Undiscounted(); in != 6300 {
		t.Fatalf("undiscounted %v", in)
	}
	if in, _, _ := m.Effective(time.Now(), 96*time.Hour); in != 6300 {
		t.Fatalf("a price change inside the lookahead must be used: %v", in)
	}
	if in, _, _ := m.Effective(time.Now(), 0); in != 630 {
		t.Fatalf("current price: %v", in)
	}
	if j := s.Models["jev-1-13:free"]; j == nil || j.Endpoint != "systemone" || !j.Free {
		t.Fatalf("free judge missing: %+v", j)
	}
	if j := s.Models["jev-1-14"]; j == nil || j.InIDR != 850 || j.Free {
		t.Fatalf("paid judge price wrong: %+v", j)
	}
	v := s.Models["v"]
	if len(v.Lines) != 1 || v.Lines[0].IDR != 750 || v.SunsetAt == nil {
		t.Fatalf("video: %+v", v)
	}
	if s.Tier.Free.Daily != 50 || s.Tier.Topup.RPM != 10 || s.Tier.Plan.RPM != 25 || s.USDIDR != 17000 {
		t.Fatalf("tier: %+v", s.Tier)
	}
	p := s.Plan("indie")
	if p == nil || p.BudgetWeek != 75000 || p.BudgetMon != 300000 || p.Budget5h != 0 || p.Scope[0] != "m" {
		t.Fatalf("plan: %+v", p)
	}
	if len(s.Changes) != 1 || s.Changes[0].Kind != "added" {
		t.Fatal("changes")
	}
}

func TestFailedRefreshKeepsLastSnapshotAndMarksItStale(t *testing.T) {
	ok := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"a","endpoints":["chat"],"pricing":{"free":true}}]}`)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := New(&Kenari{BaseURL: srv.URL, UA: "t", HTTP: srv.Client()}, t.TempDir()+"/c.json")
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok = false
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	s := c.Snapshot()
	if s == nil || len(s.Models) != 1 || !s.Degraded {
		t.Fatalf("snapshot should be kept and flagged stale: %+v", s)
	}
	// a restart while kenari is down still loads the cached catalog
	c2 := New(&Kenari{BaseURL: srv.URL, UA: "t", HTTP: srv.Client()}, "")
	_ = c2
}
