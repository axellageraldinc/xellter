package store

import (
	"bytes"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenRenamesLegacyShadowColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "xellter.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE requests(id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, client TEXT, shadow TEXT, requested TEXT, model TEXT, upstream TEXT, path TEXT, status INTEGER, ok INTEGER, latency_ms INTEGER, ttft_ms INTEGER, in_tok INTEGER, out_tok INTEGER, cached_tok INTEGER, cost REAL, billing TEXT, stream INTEGER, attempt INTEGER, canary INTEGER, rtk_saved INTEGER, err TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.Log(Request{TS: time.Now().UnixMilli(), Client: "c", Preset: "text", Model: "m", OK: true})
	st.Flush()
	rows, err := st.Usage("preset", time.Now().Add(-time.Hour), "", "")
	if err != nil || len(rows) != 1 || rows[0].Key != "text" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

// The canary flag, the first-seen dates and the judge scores are gone: an old
// database is cleaned up on open, and a new row is written without them.
func TestOpenDropsTheCanaryColumnAndTheSeenTable(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "xellter.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE requests(id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, client TEXT, preset TEXT, requested TEXT, model TEXT, upstream TEXT, path TEXT, status INTEGER, ok INTEGER, latency_ms INTEGER, ttft_ms INTEGER, in_tok INTEGER, out_tok INTEGER, cached_tok INTEGER, cost REAL, billing TEXT, stream INTEGER, attempt INTEGER, canary INTEGER, rtk_saved INTEGER, err TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE seen(model TEXT PRIMARY KEY, ts INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE jev(model TEXT PRIMARY KEY, score REAL, n INTEGER, ts INTEGER)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('requests') WHERE name='canary'`).Scan(&n) != nil || n != 0 {
		t.Fatalf("the canary column must be gone: %d", n)
	}
	if st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='seen'`).Scan(&n) != nil || n != 0 {
		t.Fatal("the seen table must be gone")
	}
	if st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='jev'`).Scan(&n) != nil || n != 0 {
		t.Fatal("the jev table must be gone")
	}
	// a request is still logged after the cleanup
	st.Log(Request{TS: time.Now().UnixMilli(), Client: "c", Preset: "text", Model: "m", OK: true})
	st.Flush()
	rows, err := st.Recent(10, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

// The session and api_key columns arrived with the rewrite. A database from before them gains the columns
// instead of being thrown away.
func TestOpenAddsTheSessionAndKeyColumns(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "xellter.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE requests(id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, client TEXT, preset TEXT, requested TEXT, model TEXT, upstream TEXT, path TEXT, status INTEGER, ok INTEGER, latency_ms INTEGER, ttft_ms INTEGER, in_tok INTEGER, out_tok INTEGER, cached_tok INTEGER, cost REAL, billing TEXT, stream INTEGER, attempt INTEGER, rtk_saved INTEGER, err TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, col := range []string{"session", "api_key"} {
		var n int
		if st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('requests') WHERE name=?`, col).Scan(&n) != nil || n != 1 {
			t.Fatalf("column %q missing: %d", col, n)
		}
	}
}

// A request row keeps its session, key, billing class and cost, and the aggregates read them back.
func TestRequestsAggregateByClientPresetModelAndKey(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	st.Log(Request{TS: now.UnixMilli(), Client: "babu", Preset: "text", Model: "cheap-b", Upstream: "babu/plan", OK: true, Billing: "plan",
		InTok: 1000, OutTok: 50, CachedTok: 400, CatalogCost: 1.5, Session: "s1", Key: "plan", RTKSaved: 120})
	st.Log(Request{TS: now.UnixMilli(), Client: "babu", Preset: "text", Model: "pro-c", Upstream: "babu/topup", Status: 503, Billing: "payg", Session: "s1", Key: "topup"})
	st.Log(Request{TS: now.UnixMilli(), Client: "aixell", Preset: "image", Model: "img-paid", OK: true, Billing: "payg", CatalogCost: 2, Key: "main"})
	st.Flush()
	since := now.Add(-time.Hour)

	// per client
	rows, err := st.Usage("client", since, "", "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("client rows=%v err=%v", rows, err)
	}
	byClient := map[string]UsageRow{}
	for _, r := range rows {
		byClient[r.Client] = r
	}
	if b := byClient["babu"]; b.Requests != 2 || b.Errors != 1 || b.Cost != 1.5 {
		t.Fatalf("babu row: %+v", b)
	}
	// per key, filtered by client
	rows, err = st.Usage("key", since, "babu", "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("key rows=%v err=%v", rows, err)
	}
	for _, r := range rows {
		if r.APIKey != "plan" && r.APIKey != "topup" {
			t.Fatalf("key row: %+v", r)
		}
		if r.APIKey == "plan" && (r.InTok != 1000 || r.OutTok != 50 || r.Cached != 400 || r.RTKSaved != 120) {
			t.Fatalf("plan key row: %+v", r)
		}
	}
	// per preset and model, which is the drill-down's last level
	rows, err = st.Usage("preset_model", since, "babu", "text")
	if err != nil || len(rows) != 2 {
		t.Fatalf("preset_model rows=%v err=%v", rows, err)
	}
	for _, r := range rows {
		if r.Preset != "text" || (r.Model != "cheap-b" && r.Model != "pro-c") {
			t.Fatalf("preset_model row: %+v", r)
		}
	}
	// another client's traffic never leaks in
	rows, _ = st.Usage("preset_model", since, "aixell", "")
	if len(rows) != 1 || rows[0].Model != "img-paid" {
		t.Fatalf("aixell rows: %+v", rows)
	}
	// an unknown grouping is refused instead of silently returning everything
	if _, err := st.Usage("nonsense", since, "", ""); err == nil {
		t.Fatal("an unknown group must be an error")
	}
}

// ModelUsage only counts successful requests: a failed attempt never reached the model, so it was never billed.
func TestModelUsageCountsOnlySuccessfulRequests(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	st.Log(Request{TS: now.UnixMilli(), Client: "babu", Model: "cheap-b", OK: true, Billing: "plan", InTok: 1000, OutTok: 50, CatalogCost: 1.5})
	st.Log(Request{TS: now.UnixMilli(), Client: "babu", Model: "cheap-b", Status: 500, OK: false, Billing: "payg"})
	st.Log(Request{TS: now.UnixMilli(), Client: "babu", Model: "pro-c", OK: true, Billing: "payg", InTok: 10, OutTok: 5, CatalogCost: 9})
	st.Flush()
	rows, err := st.ModelUsage(now.Add(-time.Hour), "babu")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ModelUsageRow{}
	for _, r := range rows {
		got[r.Model+"|"+r.Billing] = r
	}
	if len(got) != 2 || got["cheap-b|plan"].Requests != 1 || got["cheap-b|plan"].InTok != 1000 || got["pro-c|payg"].Cost != 9 {
		t.Fatalf("rows: %+v", got)
	}
	if _, ok := got["cheap-b|payg"]; ok {
		t.Fatal("a failed request must not be billed")
	}
}

// A session's traffic is read back as one record: its tokens, its cost and the models it used.
func TestSessionReadsTheSessionsTraffic(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	st.Log(Request{TS: now.Add(-time.Minute).UnixMilli(), Client: "babu", Preset: "text", Model: "cheap-b", OK: true, Billing: "plan", InTok: 100, OutTok: 10, CatalogCost: 0.5, Session: "s1"})
	st.Log(Request{TS: now.UnixMilli(), Client: "babu", Preset: "text", Model: "pro-c", OK: true, Billing: "payg", InTok: 200, OutTok: 20, CatalogCost: 1.5, Session: "s1"})
	st.Log(Request{TS: now.UnixMilli(), Client: "babu", Preset: "text", Model: "pro-c", Status: 503, Session: "s1"})
	st.Log(Request{TS: now.UnixMilli(), Client: "babu", Preset: "text", Model: "cheap-b", OK: true, Session: "other"})
	st.Flush()
	u, err := st.Session("babu", "s1")
	if err != nil || u == nil {
		t.Fatalf("session: %v %+v", err, u)
	}
	if u.Requests != 3 || u.Errors != 1 || u.InTok != 300 || u.OutTok != 30 || u.Cost != 2 {
		t.Fatalf("session totals: %+v", u)
	}
	if len(u.Models) != 2 || u.Models[0] != "cheap-b" || u.Models[1] != "pro-c" || u.LastModel != "pro-c" {
		t.Fatalf("session models: %+v", u)
	}
	if u.FirstTS == 0 || u.LastTS < u.FirstTS {
		t.Fatalf("timestamps: %+v", u)
	}
	// another client cannot read it, and an unknown session is nil rather than an empty record
	if u, err := st.Session("aixell", "s1"); err != nil || u != nil {
		t.Fatalf("a session belongs to one client: %v %+v", err, u)
	}
	if u, err := st.Session("babu", "nope"); err != nil || u != nil {
		t.Fatalf("unknown session: %v %+v", err, u)
	}
}

// The bench table is one row per (model, arena): a score from another source is a different fact, not a
// replacement. Bench returns them grouped model -> arena.
func TestBenchRoundTripsPerArena(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ReplaceBench([]BenchRow{
		{Arena: "text", Value: 0.62, Rating: 1483, Lo: 1478, Hi: 1488, Matched: "Claude Opus 4.7", Date: "2026-10-22", Votes: 12000},
		{Arena: "webdev", Value: 0.7, Rating: 1450, Date: "2026-10-22"},
	}, "claude-opus-4-7"); err != nil {
		t.Fatal(err)
	}
	got, err := st.Bench()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got["claude-opus-4-7"]) != 2 {
		t.Fatalf("bench rows: %+v", got)
	}
	if r := got["claude-opus-4-7"]["text"]; r.Value != 0.62 || r.Rating != 1483 || r.Matched != "Claude Opus 4.7" || r.Votes != 12000 {
		t.Fatalf("text row: %+v", r)
	}
	// replacing one model keeps the other models' rows
	if err := st.ReplaceAllBench(map[string][]BenchRow{"m2": {{Arena: "text", Value: 0.5}}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Bench()
	if len(got) != 1 || got["claude-opus-4-7"] != nil {
		t.Fatalf("ReplaceAllBench must swap the whole table: %+v", got)
	}
	// and one model can be re-scored without touching the rest
	if err := st.ReplaceAllBench(map[string][]BenchRow{"a": {{Arena: "text", Value: 0.1}}, "b": {{Arena: "text", Value: 0.2}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBench([]BenchRow{{Arena: "text", Value: 0.9}}, "a"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Bench()
	if got["a"]["text"].Value != 0.9 || got["b"]["text"].Value != 0.2 {
		t.Fatalf("ReplaceBench must only touch its model: %+v", got)
	}
}

// An old single-arena bench table is rebuilt instead of migrated: the new shape holds several rows per model.
func TestOpenRebuildsTheOldBenchTable(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "xellter.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE bench(model TEXT PRIMARY KEY, value REAL, date TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ReplaceBench([]BenchRow{{Arena: "text", Value: 0.5}, {Arena: "agent", Value: 0.4}}, "m"); err != nil {
		t.Fatalf("the rebuilt table must accept several rows per model: %v", err)
	}
	if got, _ := st.Bench(); len(got["m"]) != 2 {
		t.Fatalf("bench rows: %+v", got)
	}
}

func TestSessionsAndJobs(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.PutSession("h1", time.Now().Add(time.Hour), "127.0.0.1", "ua")
	if !st.SessionValid("h1") || st.SessionCount() != 1 {
		t.Fatal("session not stored")
	}
	st.PutSession("h2", time.Now().Add(-time.Hour), "127.0.0.1", "ua")
	if st.SessionValid("h2") || st.SessionCount() != 1 {
		t.Fatal("an expired session must not be valid")
	}
	st.DeleteSession("h1")
	if st.SessionValid("h1") || st.SessionCount() != 0 {
		t.Fatal("session not deleted")
	}
	st.PutJob("job-1", "babu/plan")
	if up, ok := st.Job("job-1"); !ok || up != "babu/plan" {
		t.Fatalf("job: %q %v", up, ok)
	}
	if _, ok := st.Job("nope"); ok {
		t.Fatal("unknown job")
	}
	st.KVSet("k", "v")
	if st.KVGet("k") != "v" {
		t.Fatal("kv not stored")
	}
	st.KVSet("k", "w")
	if st.KVGet("k") != "w" {
		t.Fatal("kv not replaced")
	}
}

// The request log is the only history xellter keeps, and it lives in one file.
// Deleting that file used to bring the service back as if it had never run:
// the new database started at request id 1 and the usage page showed nothing.
// A marker next to the database makes the loss visible.
func TestALostDatabaseIsDetected(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Log(Request{TS: time.Now().UnixMilli(), Client: "c", Preset: "text", Model: "m", OK: true})
	st.Flush()
	st.Close()

	// the marker says the database existed
	if _, err := os.Stat(filepath.Join(dir, lostMarker)); err != nil {
		t.Fatalf("the marker must exist: %v", err)
	}

	// the database is deleted and recreated, which is what an accidental
	// `rm` looks like
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dir, "xellter.db"+suffix)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	// the log is captured around the open, so the report is caught
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	st2, err := Open(dir)
	log.SetOutput(old)
	if err != nil {
		t.Fatalf("the service must still start: %v", err)
	}
	defer st2.Close()

	if !strings.Contains(buf.String(), "the history was lost") {
		t.Fatalf("the lost history was not reported: %q", buf.String())
	}
}

// The models that refuse a reasoning effort are written to the database, so a restart does not make the next
// request fail again.
func TestEffortFactsSurviveAReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveEffortFacts([]EffortFact{
		{Model: "stubborn", Level: "none", Verdict: "rejects", At: 1000},
		{Model: "old-measurement", Level: "none", Verdict: "ignores", At: 1000},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got := st2.EffortFacts()
	if len(got) != 1 || got[0].Model != "stubborn" || got[0].At != 1000 {
		t.Fatalf("read back %+v: only refusals come back", got)
	}
}

// Saving replaces what was there: the knowledge is the current state, not a log.
func TestSavingEffortFactsReplacesTheOldOnes(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SaveEffortFacts([]EffortFact{{Model: "a", Level: "none", Verdict: "rejects", At: 1}})
	_ = st.SaveEffortFacts([]EffortFact{{Model: "b", Level: "none", Verdict: "rejects", At: 2}})
	if got := st.EffortFacts(); len(got) != 1 || got[0].Model != "b" {
		t.Fatalf("got %+v", got)
	}
}

// A log that silently loses rows makes the usage page and the health rules lie, so the loss is counted.
func TestRowsThrownAwayAreCounted(t *testing.T) {
	s := &Store{logC: make(chan Request, 1)} // no writer: the queue fills at once
	s.Log(Request{})
	s.Log(Request{})
	s.Log(Request{})
	if dropped, lost := s.LogLoss(); dropped != 2 || lost != 0 {
		t.Fatalf("dropped %d lost %d, want 2 and 0", dropped, lost)
	}
}

func TestPresetStatsGiveTheMedianWaitAndTheFailureShare(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	for i, ttft := range []int{1000, 2000, 3000, 4000, 20000} {
		st.Log(Request{TS: now.UnixMilli() - int64(i), Client: "c", Preset: "p", Model: "m", Path: "/v1/chat/completions", Status: 200, OK: true, Stream: true, TTFTMS: ttft})
	}
	st.Log(Request{TS: now.UnixMilli(), Client: "c", Preset: "p", Model: "m", Path: "/v1/chat/completions", Status: 503, Err: "upstream error"})
	st.Log(Request{TS: now.UnixMilli(), Client: "c", Preset: "p", Model: "m", Path: "/v1/chat/completions", Err: "cancelled by the client"}) // not the model's doing
	st.Log(Request{TS: now.UnixMilli(), Client: "other", Preset: "p", Model: "m", Path: "/v1/chat/completions", Status: 200, OK: true, Stream: true, TTFTMS: 99999})
	st.Flush()
	got := st.PresetStats("c", "p", now.Add(-time.Hour))["m"]
	if got == nil || got.Requests != 6 || got.Failed != 1 || got.FailPct != 16.7 || got.TTFTp50MS != 3000 || got.TTFTp90MS != 20000 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestModelEventsAreKeptPerPresetNewestFirst(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddModelEvent(ModelEvent{TS: 1, Client: "c", Preset: "p", Model: "a", Kind: "slow", Reason: "r1"})
	st.AddModelEvent(ModelEvent{TS: 2, Client: "c", Preset: "p", Model: "b", Kind: "errors", Reason: "r2"})
	st.AddModelEvent(ModelEvent{TS: 3, Client: "c", Preset: "other", Model: "z"})
	got := st.ModelEvents("c", "p", 10)
	if len(got) != 2 || got[0].Model != "b" || got[1].Model != "a" {
		t.Fatalf("events = %+v", got)
	}
}

// X search is summarised on its own: charged answers, free give-ups, free
// retries, and the model kenari names when it names one.
func TestSearchSummary(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UnixMilli()
	st.Log(Request{TS: now - 3000, Client: "a", Model: "x/search", OK: true, Attempt: 2, LatencyMS: 30000, CatalogCost: 100})
	st.Log(Request{TS: now - 2000, Client: "a", Model: "x/search", OK: true, Attempt: 1, LatencyMS: 10000, CatalogCost: 100, UpstreamModel: "grok-x"})
	st.Log(Request{TS: now - 1000, Client: "a", Model: "x/search", Status: 503, Attempt: 3})
	st.Log(Request{TS: now, Client: "b", Model: "x/search", OK: true, Attempt: 1, CatalogCost: 100})
	st.Log(Request{TS: now, Client: "a", Model: "m", OK: true})
	st.Flush()
	got, err := st.Search(time.Now().Add(-time.Hour), "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Calls != 3 || got.OK != 2 || got.Failed != 1 || got.Retries != 3 || got.Cost != 200 || got.AvgMS != 20000 || got.Model != "grok-x" {
		t.Fatalf("got %+v", got)
	}
	if all, _ := st.Search(time.Now().Add(-time.Hour), ""); all.Calls != 4 {
		t.Fatalf("all clients: %+v", all)
	}
}

// MediaSpend sums units per kind of work; an old media row and an X search are told apart by what they were, and a chat row or a failed call is not counted at all.
func TestMediaSpendSeparatesUnitsLegacyAndServices(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UnixMilli()
	st.Log(Request{TS: now, Client: "c", Preset: "pic", Model: "img", Path: "/v1/images/generations", OK: true, CatalogCost: 3, Unit: "image", Units: 2})
	st.Log(Request{TS: now, Client: "c", Preset: "pic", Model: "img", Path: "/v1/images/generations", OK: true, CatalogCost: 1, Unit: "image", Units: 1})
	st.Log(Request{TS: now, Client: "c", Preset: "pic", Model: "img", Path: "/v1/images/generations", OK: true, CatalogCost: 7}) // before units: one image
	st.Log(Request{TS: now, Client: "c", Preset: "tts", Model: "voice", Path: "/v1/audio/speech", OK: true, CatalogCost: 2})     // before units: unknown amount
	st.Log(Request{TS: now, Client: "c", Preset: "", Model: "x/search", Path: "/v1/x/search", OK: true, CatalogCost: 5})
	st.Log(Request{TS: now, Client: "c", Preset: "pic", Model: "img", Path: "/v1/images/generations", OK: false, Unit: "image", Units: 9})
	st.Log(Request{TS: now, Client: "c", Preset: "text", Model: "m", Path: "/v1/chat/completions", OK: true, CatalogCost: 11})
	st.Flush()
	got, err := st.MediaSpend("client", time.Now().Add(-time.Hour), "", "")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]UnitSpend{}
	for _, u := range got["c"] {
		by[u.Unit] = u
	}
	if len(by) != 3 || by["image"].Units != 4 || by["image"].Cost != 11 || by["legacy"].Cost != 2 || by["service"].Cost != 5 {
		t.Fatalf("got %+v", got)
	}
}
