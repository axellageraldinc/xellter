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

// searchPriceMicroIDR is what one successful search call costs the balance.
// kenari takes the money itself; xellter only records the same number in its
// log, so the usage page and the panel show what the call was worth.
const searchPriceMicroIDR = 100_000_000

// searchAttempts is how many times a search is tried. kenari documents a 503 as
// "X search failed to return a usable answer", a call that is not charged and
// that the caller should retry, so a retry costs nothing and turns the
// intermittent failures into answers.
const searchAttempts = 3

// searchBackoff is the wait before attempt 2 and attempt 3.
var searchBackoff = []time.Duration{700 * time.Millisecond, 1800 * time.Millisecond}

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
	start := s.Now()
	var status int
	var raw []byte
	var ctype string
	attempts := 0
	for attempt := 1; attempt <= searchAttempts; attempt++ {
		attempts = attempt
		if attempt > 1 {
			select {
			case <-time.After(searchBackoff[attempt-2]):
			case <-r.Context().Done():
				return
			}
		}
		var res *http.Response
		res, err = s.sendSearch(r, s.conn(cfg, key.Provider()), key.Secret(), body)
		if err != nil {
			// The client went away: not the search's failure, and not ours to retry.
			if r.Context().Err() != nil {
				return
			}
			break
		}
		status, ctype = res.StatusCode, res.Header.Get("Content-Type")
		raw, _ = io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		// 503 is kenari's own "could not answer, not charged, retry"; 429 asks the
		// same. Anything else is the answer, and it goes to the client untouched.
		if status != 503 && status != 429 {
			break
		}
	}

	row := store.Request{TS: start.UnixMilli(), Client: cl.Name, Requested: "x/search", Model: "x/search",
		Upstream: cl.Name + "/" + key.Label(), Path: r.URL.Path, Billing: "payg", Attempt: attempts, Key: cl.KeyHint}
	row.Status = status
	row.OK = status >= 200 && status < 300
	row.LatencyMS = int(s.Now().Sub(start) / time.Millisecond)
	if row.OK {
		row.CatalogCost = float64(searchPriceMicroIDR) / 1e6
	} else if err != nil {
		row.Err = "network: " + trimErr(err)
	} else {
		row.Err = "upstream " + strconv.Itoa(status)
	}
	s.st.Log(row)

	if err != nil {
		writeErr(w, r.URL.Path, 502, "upstream_unavailable", trimErr(err), 5*time.Second)
		return
	}
	// The answer is the upstream's own, in its own words: the client asked
	// kenari, so the client gets what kenari said, including the error.
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("X-Xellter-Attempt", strconv.Itoa(attempts))
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// sendSearch makes one attempt against the upstream search endpoint.
func (s *Server) sendSearch(r *http.Request, conn config.ProviderConn, secret string, body []byte) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, conn.BaseURL+"/v1/x/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("User-Agent", conn.UserAgent)
	res, err := s.hc.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	// The caller reads the body and closes it; the cancel rides along until then.
	res.Body = &cancelledBody{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

// cancelledBody releases the request context once the body is closed.
type cancelledBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelledBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
