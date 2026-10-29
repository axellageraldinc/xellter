package app_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axellageraldinc/xellter/internal/app"
	"github.com/axellageraldinc/xellter/internal/catalog"
	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
)

// logSpend writes request rows for a client: n rows of cost each, at the given time.
func logSpend(e *env, client string, at time.Time, n int, cost float64) {
	for i := 0; i < n; i++ {
		e.a.St.Log(store.Request{TS: at.UnixMilli(), Client: client, Preset: "text", Model: "cheap-b", Status: 200, OK: true, CatalogCost: cost})
	}
}

// A client whose last hour costs far more than its usual hour is reported; a client with an ordinary hour is not.
func TestASpendSpikeIsReported(t *testing.T) {
	e := setup(t, nil)
	now := time.Now()
	// babu spends 1,000 an hour for a week, then 200,000 in the last hour.
	for h := 2; h < 7*24; h++ {
		logSpend(e, "babu", now.Add(-time.Duration(h)*time.Hour), 1, 1000)
	}
	logSpend(e, "babu", now.Add(-10*time.Minute), 4, 50000)
	// locked has the same ordinary hour as always.
	for h := 2; h < 7*24; h++ {
		logSpend(e, "locked", now.Add(-time.Duration(h)*time.Hour), 1, 60000)
	}
	logSpend(e, "locked", now.Add(-10*time.Minute), 1, 60000)

	e.a.St.Flush()
	got := e.a.SpendSpikes(now)
	if len(got) != 1 || got[0].Client != "babu" {
		t.Fatalf("spikes: %+v", got)
	}
}

// The webhook gets one JSON message per spike, and the same spike is not sent again within six hours.
func TestASpendSpikeIsSentOnceToTheWebhook(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	e := setup(t, func(c *config.Config) { c.Alerts.WebhookURL = hook.URL })
	now := time.Now()
	logSpend(e, "babu", now.Add(-10*time.Minute), 4, 50000)

	e.a.St.Flush()
	e.a.NotifySpikes(t.Context(), now)
	e.a.NotifySpikes(t.Context(), now.Add(time.Minute))
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("webhook calls: %d", len(bodies))
	}
	var m struct {
		Text   string  `json:"text"`
		Client string  `json:"client"`
		Hour   float64 `json:"hour_idr"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &m); err != nil || m.Client != "babu" || m.Hour != 200000 || !strings.Contains(m.Text, "babu") {
		t.Fatalf("body %s: %v", bodies[0], err)
	}
}

// A restart does not send the same alert again: the send times are kept in the database.
func TestASentAlertSurvivesARestart(t *testing.T) {
	var mu sync.Mutex
	n := 0
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); n++; mu.Unlock() }))
	defer hook.Close()
	e := setup(t, func(c *config.Config) { c.Alerts.WebhookURL = hook.URL })
	now := time.Now()
	logSpend(e, "babu", now.Add(-10*time.Minute), 4, 50000)
	e.a.St.Flush()
	e.a.NotifySpikes(t.Context(), now)

	// a second app over the same data directory is what a restart is
	e.a.Close()
	b, err := app.New(e.path, &catalog.Kenari{BaseURL: e.f.srv.URL, UA: "test", HTTP: e.f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.NotifySpikes(t.Context(), now.Add(time.Minute))
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Fatalf("webhook calls: %d, want 1", n)
	}
}
