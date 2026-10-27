// Package store is the SQLite-backed state: request log, model health,
// leaderboard scores, admin sessions and async job routing.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Request is one logged proxy request. No prompt or response content is stored.
type Request struct {
	TS          int64 // unix ms
	Client      string
	Preset      string
	Requested   string
	Model       string
	Upstream    string
	Path        string
	Status      int
	OK          bool
	LatencyMS   int
	TTFTMS      int
	InTok       int
	OutTok      int
	CachedTok   int
	CatalogCost float64 // IDR at catalog prices
	Billing     string  // free | plan | payg
	Stream      bool
	Attempt     int
	RTKSaved    int
	Err         string
	Session     string // session id the request belonged to ("" when none)
	Key         string // hint of the xellter API key the caller used (never the key itself)
}

// ModelStat is aggregated health of one model.
type ModelStat struct {
	N         int
	OK        int
	AvgLat    float64
	AvgTTFT   float64
	LastError string
}

// Store wraps the database.
type Store struct {
	db   *sql.DB
	logC chan Request
	wg   sync.WaitGroup
	stop chan struct{}
}

const schema = `
CREATE TABLE IF NOT EXISTS requests(
 id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, client TEXT, preset TEXT, requested TEXT, model TEXT, upstream TEXT, path TEXT,
 status INTEGER, ok INTEGER, latency_ms INTEGER, ttft_ms INTEGER, in_tok INTEGER, out_tok INTEGER, cached_tok INTEGER,
 cost REAL, billing TEXT, stream INTEGER, attempt INTEGER, rtk_saved INTEGER, err TEXT, session TEXT, api_key TEXT);
CREATE INDEX IF NOT EXISTS req_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS req_model_ts ON requests(model, ts);
CREATE INDEX IF NOT EXISTS req_up_ts ON requests(upstream, ts);
CREATE INDEX IF NOT EXISTS req_client_ts ON requests(client, ts);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, exp INTEGER NOT NULL, ip TEXT, ua TEXT, created INTEGER);
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY, upstream TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE IF NOT EXISTS bench(model TEXT NOT NULL, arena TEXT NOT NULL, value REAL, rating REAL, lo REAL, hi REAL, matched TEXT, date TEXT, votes REAL, PRIMARY KEY(model, arena));
`

// Open creates or opens the database in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "xellter.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	// The request log and the leaderboard scores are the only history xellter
	// keeps, and they live in this one file. Deleting it silently throws them
	// away and the service comes back as if it had never run, so a marker file
	// next to the database records that it existed: a database that reappears
	// without its marker means the file was lost, and the operator is told
	// (see checkLost).
	if err := checkLost(dir, db); err != nil {
		return nil, err
	}
	// databases created before the "shadow" -> "preset" rename
	var n int
	if db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('requests') WHERE name='shadow'`).Scan(&n) == nil && n > 0 {
		if _, err := db.Exec(`ALTER TABLE requests RENAME COLUMN shadow TO preset`); err != nil {
			return nil, err
		}
	}
	// the trial mechanism is gone: the canary flag and the first-seen dates are no longer used
	if db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('requests') WHERE name='canary'`).Scan(&n) == nil && n > 0 {
		if _, err := db.Exec(`ALTER TABLE requests DROP COLUMN canary`); err != nil {
			return nil, err
		}
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS seen`); err != nil {
		return nil, err
	}
	// judging is gone: quality comes from leaderboards and configured benchmarks only,
	// so the stored scores and the "_system" rows the judge wrote are dropped
	if _, err := db.Exec(`DROP TABLE IF EXISTS jev`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`DELETE FROM requests WHERE client='_system'`); err != nil {
		return nil, err
	}
	// the session and api_key columns arrived with the rewrite: the panel's per-session and per-key views need them
	for _, col := range []string{"session", "api_key"} {
		if db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('requests') WHERE name=?`, col).Scan(&n) == nil && n == 0 {
			if _, err := db.Exec(`ALTER TABLE requests ADD COLUMN ` + col + ` TEXT`); err != nil {
				return nil, err
			}
		}
	}
	// the session index needs the column above, so it is created after the migration, not in the schema
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS req_session ON requests(session, ts)`); err != nil {
		return nil, err
	}
	// the bench table became one row per (model, arena): a score from another source is a different fact, not a
	// replacement. The old shape held one arena per model, so it is rebuilt rather than migrated.
	if db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('bench') WHERE name='model'`).Scan(&n) == nil {
		var pk int
		_ = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('bench') WHERE pk>0`).Scan(&pk)
		if pk < 2 {
			if _, err := db.Exec(`DROP TABLE IF EXISTS bench`); err != nil {
				return nil, err
			}
			if _, err := db.Exec(schema); err != nil {
				return nil, err
			}
		}
	}
	s := &Store{db: db, logC: make(chan Request, 1024), stop: make(chan struct{})}
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

// Close flushes the log queue and closes the database.
func (s *Store) Close() error {
	close(s.stop)
	s.wg.Wait()
	return s.db.Close()
}

// Log queues a request row; it never blocks the hot path (drops when full).
func (s *Store) Log(r Request) {
	select {
	case s.logC <- r:
	default:
	}
}

func (s *Store) writer() {
	defer s.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var batch []Request
	flush := func() {
		if len(batch) == 0 {
			return
		}
		tx, err := s.db.Begin()
		if err != nil {
			batch = batch[:0]
			return
		}
		st, err := tx.Prepare(`INSERT INTO requests(ts,client,preset,requested,model,upstream,path,status,ok,latency_ms,ttft_ms,in_tok,out_tok,cached_tok,cost,billing,stream,attempt,rtk_saved,err,session,api_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err == nil {
			for _, r := range batch {
				_, _ = st.Exec(r.TS, r.Client, r.Preset, r.Requested, r.Model, r.Upstream, r.Path, r.Status, b2i(r.OK), r.LatencyMS, r.TTFTMS, r.InTok, r.OutTok, r.CachedTok, r.CatalogCost, r.Billing, b2i(r.Stream), r.Attempt, r.RTKSaved, r.Err, r.Session, r.Key)
			}
			st.Close()
		}
		_ = tx.Commit()
		batch = batch[:0]
	}
	for {
		select {
		case r := <-s.logC:
			batch = append(batch, r)
			if len(batch) >= 200 {
				flush()
			}
		case <-t.C:
			flush()
		case <-s.stop:
			for {
				select {
				case r := <-s.logC:
					batch = append(batch, r)
				default:
					flush()
					return
				}
			}
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Flush waits until queued rows are written (used by tests and shutdown).
func (s *Store) Flush() {
	for i := 0; i < 50 && len(s.logC) > 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(1100 * time.Millisecond)
}

// Stats aggregates per-model health over the window.
func (s *Store) Stats(since time.Time) (map[string]ModelStat, error) {
	rows, err := s.db.Query(`SELECT model, COUNT(*), SUM(ok), AVG(CASE WHEN ok=1 THEN latency_ms END), AVG(CASE WHEN ok=1 AND ttft_ms>0 THEN ttft_ms END) FROM requests WHERE ts>=? AND model!='' GROUP BY model`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ModelStat{}
	for rows.Next() {
		var m string
		var st ModelStat
		var ok sql.NullInt64
		var lat, ttft sql.NullFloat64
		if err := rows.Scan(&m, &st.N, &ok, &lat, &ttft); err != nil {
			return nil, err
		}
		st.OK = int(ok.Int64)
		st.AvgLat, st.AvgTTFT = lat.Float64, ttft.Float64
		out[m] = st
	}
	return out, rows.Err()
}

// FreeTimes returns request timestamps (ms) of an upstream since t for free-model traffic.
func (s *Store) FreeTimes(upstream string, since time.Time) ([]int64, error) {
	return s.times(`SELECT ts FROM requests WHERE upstream=? AND ts>=? AND billing='free' ORDER BY ts`, upstream, since)
}

// AllTimes returns all request timestamps (ms) of an upstream since t.
func (s *Store) AllTimes(upstream string, since time.Time) ([]int64, error) {
	return s.times(`SELECT ts FROM requests WHERE upstream=? AND ts>=? ORDER BY ts`, upstream, since)
}

func (s *Store) times(q, upstream string, since time.Time) ([]int64, error) {
	rows, err := s.db.Query(q, upstream, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var t int64
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PlanSpend returns (ts, catalogCost) of plan-billed requests of an upstream since t.
func (s *Store) PlanSpend(upstream string, since time.Time) ([]TimedCost, error) {
	rows, err := s.db.Query(`SELECT ts, cost FROM requests WHERE upstream=? AND ts>=? AND billing='plan' ORDER BY ts`, upstream, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimedCost
	for rows.Next() {
		var c TimedCost
		if err := rows.Scan(&c.TS, &c.Cost); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TimedCost is a spend sample.
type TimedCost struct {
	TS   int64
	Cost float64
}

// ClientSpend sums catalog cost of a client since t.
func (s *Store) ClientSpend(client string, since time.Time) (float64, error) {
	var v sql.NullFloat64
	err := s.db.QueryRow(`SELECT SUM(cost) FROM requests WHERE client=? AND ts>=?`, client, since.UnixMilli()).Scan(&v)
	return v.Float64, err
}

// UsageRow is one aggregate row: the dashboard's chart buckets and the usage drill-down (client, preset,
// model, key) all use this shape. Only the fields of the requested grouping are filled.
type UsageRow struct {
	Key      string  `json:"key,omitempty"` // the bucket or dimension value
	Client   string  `json:"client,omitempty"`
	Preset   string  `json:"preset,omitempty"`
	Model    string  `json:"model,omitempty"`
	APIKey   string  `json:"api_key,omitempty"`
	Billing  string  `json:"billing,omitempty"`
	Requests int     `json:"requests"`
	Errors   int     `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Cached   int64   `json:"cached_tok"`
	Cost     float64 `json:"cost_idr"`
	AvgLat   float64 `json:"avg_latency_ms"`
	RTKSaved int64   `json:"rtk_saved"`
}

// Usage groups requests. group is one of client, model, upstream, preset, key, hour, minute, day, or
// preset_model (one row per preset and model, which is the usage drill-down's last level). client and preset
// filter the rows ("" = no filter).
func (s *Store) Usage(group string, since time.Time, client, preset string) ([]UsageRow, error) {
	col := map[string]string{
		"client": "client", "model": "model", "upstream": "upstream", "preset": "preset", "key": "api_key",
		"preset_model": "preset||'|'||model",
		"hour":         "strftime('%Y-%m-%d %H:00', ts/1000, 'unixepoch')",
		"minute":       "strftime('%Y-%m-%d %H:%M', ts/1000, 'unixepoch')",
		"day":          "strftime('%Y-%m-%d', ts/1000, 'unixepoch')",
	}[group]
	if col == "" {
		return nil, errors.New("bad group")
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT %s k, COUNT(*), COUNT(*)-COALESCE(SUM(ok),0), COALESCE(SUM(in_tok),0),
		COALESCE(SUM(out_tok),0), COALESCE(SUM(cached_tok),0), COALESCE(SUM(cost),0), COALESCE(AVG(latency_ms),0), COALESCE(SUM(rtk_saved),0)
		FROM requests WHERE ts>=? AND (?='' OR client=?) AND (?='' OR preset=?) GROUP BY k ORDER BY k`,
		col), since.UnixMilli(), client, client, preset, preset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var u UsageRow
		var k sql.NullString
		if err := rows.Scan(&k, &u.Requests, &u.Errors, &u.InTok, &u.OutTok, &u.Cached, &u.Cost, &u.AvgLat, &u.RTKSaved); err != nil {
			return nil, err
		}
		u.Key = k.String
		switch group {
		case "client":
			u.Client = u.Key
		case "model":
			u.Model = u.Key
		case "preset":
			u.Preset = u.Key
		case "key":
			u.APIKey = u.Key
		case "preset_model":
			if p, m, ok := strings.Cut(u.Key, "|"); ok {
				u.Preset, u.Model = p, m
			}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ModelUsageRow is one model's traffic over a window, split by billing kind.
type ModelUsageRow struct {
	Model    string
	Billing  string
	Requests int
	InTok    int64
	OutTok   int64
	Cached   int64
	Cost     float64 // IDR at catalog prices
}

// ModelUsage aggregates a client's traffic per model and billing kind over the
// window. Only successful requests count: a failed attempt never reached the
// model, so it was never billed.
func (s *Store) ModelUsage(since time.Time, client string) ([]ModelUsageRow, error) {
	rows, err := s.db.Query(`SELECT model, billing, COUNT(*), COALESCE(SUM(in_tok),0), COALESCE(SUM(out_tok),0), COALESCE(SUM(cached_tok),0), COALESCE(SUM(cost),0)
		FROM requests WHERE ts>=? AND ok=1 AND model!='' AND (?='' OR client=?) GROUP BY model, billing`, since.UnixMilli(), client, client)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelUsageRow{}
	for rows.Next() {
		var r ModelUsageRow
		var m, b sql.NullString
		if err := rows.Scan(&m, &b, &r.Requests, &r.InTok, &r.OutTok, &r.Cached, &r.Cost); err != nil {
			return nil, err
		}
		r.Model, r.Billing = m.String, b.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentRow is a request for the log view.
type RecentRow struct {
	TS        int64   `json:"ts"`
	Client    string  `json:"client"`
	Preset    string  `json:"preset"`
	Requested string  `json:"requested"`
	Model     string  `json:"model"`
	Upstream  string  `json:"upstream"`
	Status    int     `json:"status"`
	OK        bool    `json:"ok"`
	LatencyMS int     `json:"latency_ms"`
	TTFTMS    int     `json:"ttft_ms"`
	InTok     int     `json:"in_tok"`
	OutTok    int     `json:"out_tok"`
	Cost      float64 `json:"cost_idr"`
	Billing   string  `json:"billing"`
	Attempt   int     `json:"attempt"`
	RTKSaved  int     `json:"rtk_saved"`
	Err       string  `json:"err"`
}

// Recent returns the newest rows first.
func (s *Store) Recent(limit int, client string) ([]RecentRow, error) {
	rows, err := s.db.Query(`SELECT ts,client,preset,requested,model,upstream,status,ok,latency_ms,ttft_ms,in_tok,out_tok,cost,billing,attempt,rtk_saved,err FROM requests WHERE (?='' OR client=?) ORDER BY id DESC LIMIT ?`, client, client, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecentRow{}
	for rows.Next() {
		var r RecentRow
		var ok int
		var cl, sh, rq, mo, up, bi, er sql.NullString
		if err := rows.Scan(&r.TS, &cl, &sh, &rq, &mo, &up, &r.Status, &ok, &r.LatencyMS, &r.TTFTMS, &r.InTok, &r.OutTok, &r.Cost, &bi, &r.Attempt, &r.RTKSaved, &er); err != nil {
			return nil, err
		}
		r.Client, r.Preset, r.Requested, r.Model, r.Upstream, r.Billing, r.Err = cl.String, sh.String, rq.String, mo.String, up.String, bi.String, er.String
		r.OK = ok == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// Purge deletes request rows older than the retention and expired sessions/jobs.
func (s *Store) Purge(retentionDays int) {
	cut := time.Now().AddDate(0, 0, -retentionDays).UnixMilli()
	_, _ = s.db.Exec(`DELETE FROM requests WHERE ts<?`, cut)
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE exp<?`, time.Now().Unix())
	_, _ = s.db.Exec(`DELETE FROM jobs WHERE ts<?`, time.Now().AddDate(0, 0, -3).Unix())
}

// ---- sessions ----

// PutSession stores a login session by token hash.
func (s *Store) PutSession(idHash string, exp time.Time, ip, ua string) {
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO sessions(id,exp,ip,ua,created) VALUES(?,?,?,?,?)`, idHash, exp.Unix(), ip, ua, time.Now().Unix())
}

// SessionValid reports whether the session exists and is unexpired.
func (s *Store) SessionValid(idHash string) bool {
	var exp int64
	if err := s.db.QueryRow(`SELECT exp FROM sessions WHERE id=?`, idHash).Scan(&exp); err != nil {
		return false
	}
	return exp > time.Now().Unix()
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(idHash string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE id=?`, idHash)
}

// DeleteAllSessions logs everyone out.
func (s *Store) DeleteAllSessions() { _, _ = s.db.Exec(`DELETE FROM sessions`) }

// SessionCount returns active sessions.
func (s *Store) SessionCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE exp>?`, time.Now().Unix()).Scan(&n)
	return n
}

// ---- async jobs (video) ----

// PutJob remembers which upstream owns an async job id.
func (s *Store) PutJob(id, upstream string) {
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO jobs(id,upstream,ts) VALUES(?,?,?)`, id, upstream, time.Now().Unix())
}

// Job returns the upstream that owns a job id.
func (s *Store) Job(id string) (string, bool) {
	var u string
	if err := s.db.QueryRow(`SELECT upstream FROM jobs WHERE id=?`, id).Scan(&u); err != nil {
		return "", false
	}
	return u, true
}

// ---- kv ----

// KVGet reads a value.
func (s *Store) KVGet(k string) string {
	var v string
	_ = s.db.QueryRow(`SELECT v FROM kv WHERE k=?`, k).Scan(&v)
	return v
}

// KVSet writes a value.
func (s *Store) KVSet(k, v string) {
	_, _ = s.db.Exec(`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
}

// ---- benchmark scores (mapped from public leaderboards, see package bench) ----

// BenchRow is one published score of one catalog model in one arena. A model has several rows: one per
// leaderboard it appears on (text, webdev, agent, text_to_image, swebench, ...).
type BenchRow struct {
	Value   float64 `json:"value"` // 0..1, higher is better
	Rating  float64 `json:"rating"`
	Lo      float64 `json:"lo"`
	Hi      float64 `json:"hi"`
	Arena   string  `json:"arena"`
	Matched string  `json:"matched"`
	Date    string  `json:"date"`
	Votes   float64 `json:"votes"`
}

// ReplaceBench swaps the whole score table.
func (s *Store) ReplaceBench(rows []BenchRow, model string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM bench WHERE model=?`, model); err != nil {
		tx.Rollback()
		return err
	}
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT INTO bench(model,arena,value,rating,lo,hi,matched,date,votes) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(model,arena) DO UPDATE SET value=excluded.value, rating=excluded.rating, lo=excluded.lo, hi=excluded.hi,
			matched=excluded.matched, date=excluded.date, votes=excluded.votes`,
			model, r.Arena, r.Value, r.Rating, r.Lo, r.Hi, r.Matched, r.Date, r.Votes); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ReplaceAllBench swaps every stored score in one transaction.
func (s *Store) ReplaceAllBench(byModel map[string][]BenchRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM bench`); err != nil {
		tx.Rollback()
		return err
	}
	for model, rows := range byModel {
		for _, r := range rows {
			if _, err := tx.Exec(`INSERT INTO bench(model,arena,value,rating,lo,hi,matched,date,votes) VALUES(?,?,?,?,?,?,?,?,?)`,
				model, r.Arena, r.Value, r.Rating, r.Lo, r.Hi, r.Matched, r.Date, r.Votes); err != nil {
				tx.Rollback()
				return err
			}
		}
	}
	return tx.Commit()
}

// Bench returns the stored scores, grouped model -> arena -> row.
func (s *Store) Bench() (map[string]map[string]BenchRow, error) {
	rows, err := s.db.Query(`SELECT model,arena,value,rating,lo,hi,matched,date,votes FROM bench`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]BenchRow{}
	for rows.Next() {
		var m string
		var r BenchRow
		var matched, date sql.NullString
		if err := rows.Scan(&m, &r.Arena, &r.Value, &r.Rating, &r.Lo, &r.Hi, &matched, &date, &r.Votes); err != nil {
			return nil, err
		}
		r.Matched, r.Date = matched.String, date.String
		if out[m] == nil {
			out[m] = map[string]BenchRow{}
		}
		out[m][r.Arena] = r
	}
	return out, rows.Err()
}

// SessionUsage is what one session has done so far: the models it used and its token totals. A session is
// pinned to one model, but the rows can span several when a model was evicted mid-session.
type SessionUsage struct {
	Session   string   `json:"session"`
	Client    string   `json:"client"`
	Preset    string   `json:"preset"`
	Models    []string `json:"models"` // most recent last
	Requests  int      `json:"requests"`
	Errors    int      `json:"errors"`
	InTok     int64    `json:"in_tok"`
	OutTok    int64    `json:"out_tok"`
	Cached    int64    `json:"cached_tok"`
	Cost      float64  `json:"cost_idr"`
	FirstTS   int64    `json:"first_ts"`
	LastTS    int64    `json:"last_ts"`
	LastModel string   `json:"last_model"`
}

// Session reads one session's traffic for a client.
func (s *Store) Session(client, session string) (*SessionUsage, error) {
	rows, err := s.db.Query(`SELECT preset, model, ok, in_tok, out_tok, cached_tok, cost, ts FROM requests
		WHERE client=? AND session=? AND session!='' ORDER BY ts`, client, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	u := &SessionUsage{Session: session, Client: client}
	seen := map[string]bool{}
	for rows.Next() {
		var preset, model sql.NullString
		var ok, in, out, cached int64
		var cost float64
		var ts int64
		if err := rows.Scan(&preset, &model, &ok, &in, &out, &cached, &cost, &ts); err != nil {
			return nil, err
		}
		if u.Preset == "" {
			u.Preset = preset.String // the session's preset: one per session, even if the model was evicted
		}
		u.Requests++
		if ok == 0 {
			u.Errors++
		}
		u.InTok += in
		u.OutTok += out
		u.Cached += cached
		u.Cost += cost
		if u.FirstTS == 0 {
			u.FirstTS = ts
		}
		u.LastTS = ts
		if model.String != "" && model.String != u.LastModel {
			if !seen[model.String] {
				seen[model.String] = true
				u.Models = append(u.Models, model.String)
			}
			u.LastModel = model.String
		}
	}
	if u.Requests == 0 {
		return nil, nil
	}
	return u, rows.Err()
}

// ---- lost-database marker ----

// lostMarker is the file that records that the database existed. It sits next
// to the database but outside it, so deleting the database alone leaves the
// marker behind and the loss is visible.
const lostMarker = "xellter.db.marker"

// checkLost reports a database that was lost: the marker says there was one,
// and the database in front of it holds no request at all. That is what a
// deleted-and-recreated file looks like, and it must not pass silently.
func checkLost(dir string, db *sql.DB) error {
	marker := filepath.Join(dir, lostMarker)
	st, err := os.Stat(marker)
	if err != nil {
		if os.IsNotExist(err) {
			// The first run: record that the database is now the real one.
			return writeMarker(marker)
		}
		return err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	// The database is empty but the marker says it was not. The rows are gone.
	// The service still starts: the owner may have emptied it on purpose, and a
	// gateway that refuses to start is a worse outcome than a warning.
	log.Printf("xellter: the request log in %s is empty but the database existed since %s: the history was lost (the database file was deleted or replaced)",
		dir, st.ModTime().Format(time.RFC3339))
	return writeMarker(marker)
}

// writeMarker records the database's creation time, so a later loss is dated.
func writeMarker(path string) error {
	if err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	return nil
}
