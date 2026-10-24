package app_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// BenchmarkProxyOverhead measures one non-streaming chat request through xellter
// against a fake in-process upstream, next to the same request sent directly.
// Run: go test -bench Proxy -benchmem ./internal/app/
func BenchmarkProxyOverhead(b *testing.B) {
	e := setup(b, nil)
	body := `{"model":"%s","messages":[{"role":"user","content":"hi"}]}`
	run := func(url, key, model string) func(*testing.B) {
		return func(b *testing.B) {
			b.ReportAllocs()
			cl := &http.Client{}
			for i := 0; i < b.N; i++ {
				req, _ := http.NewRequest("POST", url+"/v1/chat/completions", strings.NewReader(strings.Replace(body, "%s", model, 1)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+key)
				resp, err := cl.Do(req)
				if err != nil {
					b.Fatal(err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}
	}
	b.Run("direct", run(e.f.srv.URL, "x", "cheap-b"))
	b.Run("via-xellter", run(e.px.URL, e.keyCl, "balanced"))
}
