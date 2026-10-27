package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/axellageraldinc/xellter/internal/config"
	"github.com/axellageraldinc/xellter/internal/store"
)

// The X search endpoint.
//
// The search is a service of kenari, not a model: kenari's catalogue lists no
// search model, so there is nothing to rank and nothing to route. A client asks
// for the endpoint and the request goes to the key that is dedicated to it.
//
// That dedicated key is the reason this path exists in its own right. The key
// is pay-as-you-go, so it is never probed and never takes part in model
// routing: xellter asks kenari about the models a key may call, and a search
// key must never pay for that curiosity. The key is used for the one call the
// client asked for.
//
// The request is forwarded as it came: kenari owns the shape of the request and
// of the answer, and xellter does not second-guess it.

// searchPriceMicroIDR is what one search call costs the balance. kenari takes
// the money itself; xellter only records the same number in its log, so the
// usage page and the panel show what the call was worth.
const searchPriceMicroIDR = 100_000_000

// search answers POST /v1/x/search. It picks the key of the client that is
// dedicated to the search service and forwards the request to it.
func (s *Server) search(w http.ResponseWriter, r *http.Request, cl *config.Client) {
	keys := s.pool.ClientCapabilityKeys(cl.Name, "search")
	if len(keys) == 0 {
		writeErr(w, r.URL.Path, 404, "no_search_key", "this client has no key for the search service", 0)
		return
	}
	key := keys[0]
	if key.Secret() == "" {
		writeErr(w, r.URL.Path, 404, "no_search_key", "the search key of this client has no value", 0)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeErr(w, r.URL.Path, 400, "bad_request", "cannot read the request body", 0)
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeErr(w, r.URL.Path, 400, "bad_request", "empty body", 0)
		return
	}

	cfg := s.cfg.Load()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Kenari.BaseURL+"/v1/x/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Secret())
	req.Header.Set("User-Agent", cfg.Kenari.UserAgent)

	start := s.Now()
	resp, err := s.hc.Do(req)
	if err != nil {
		writeErr(w, r.URL.Path, 502, "upstream_unavailable", trimErr(err), 5*time.Second)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	// The answer is the upstream's own, in its own words: the client asked
	// kenari, so the client gets what kenari said, including the error.
	row := store.Request{TS: start.UnixMilli(), Client: cl.Name, Requested: "x/search", Model: "x/search",
		Upstream: cl.Name + "/" + key.Label(), Path: r.URL.Path, Billing: "payg", Attempt: 1, Key: cl.KeyHint}
	row.Status = resp.StatusCode
	row.OK = resp.StatusCode < 300
	row.LatencyMS = int(s.Now().Sub(start) / time.Millisecond)
	if row.OK {
		row.CatalogCost = float64(searchPriceMicroIDR) / 1e6
	} else {
		row.Err = "upstream " + strconv.Itoa(resp.StatusCode)
	}
	s.st.Log(row)

	if v := resp.Header.Get("Content-Type"); v != "" {
		w.Header().Set("Content-Type", v)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}
