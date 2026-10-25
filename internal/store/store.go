// Package store is the SQLite-backed state: request log, model health,
// first-seen dates, JEV scores, admin sessions and async job routing.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

// ModelStat is aggregated health of one model.
type ModelStat struct {
	N         int
	OK        int
	AvgLat    float64
	AvgTTFT   float64
	LastError string
}

// JEVScore is the stored judge result of a model.
type JEVScore struct {
	Score float64
	N     int
	TS    time.Time
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
 cost REAL, billing TEXT, stream INTEGER, attempt INTEGER, rtk_saved INTEGER, err TEXT);
CREATE INDEX IF NOT EXISTS req_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS req_model_ts ON requests(model, ts);
CREATE INDEX IF NOT EXISTS req_up_ts ON requests(upstream, ts);
CREATE INDEX IF NOT EXISTS req_client_ts ON requests(client, ts);
CREATE TABLE IF NOT EXISTS jev(model TEXT PRIMARY KEY, score REAL, n INTEGER, ts INTEGER);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, exp INTEGER NOT NULL, ip TEXT, ua TEXT, created INTEGER);
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY, upstream TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE IF NOT EXISTS bench(model TEXT PRIMARY KEY, value REAL, rating REAL, lo REAL, hi REAL, arena TEXT, matched TEXT, date TEXT, votes REAL);
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
		st, err := tx.Prepare(`INSERT INTO requests(ts,client,preset,requested,model,upstream,path,status,ok,latency_ms,ttft_ms,in_tok,out_tok,cached_tok,cost,billing,stream,attempt,rtk_saved,err) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err == nil {
			for _, r := range batch {
				_, _ = st.Exec(r.TS, r.Client, r.Preset, r.Requested, r.Model, r.Upstream, r.Path, r.Status, b2i(r.OK), r.LatencyMS, r.TTFTMS, r.InTok, r.OutTok, r.CachedTok, r.CatalogCost, r.Billing, b2i(r.Stream), r.Attempt, r.RTKSaved, r.Err)
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

// UsageRow is one aggregate row for the dashboard.
type UsageRow struct {
	Key      string  `json:"key"`
	Requests int     `json:"requests"`
	Errors   int     `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Cost     float64 `json:"cost_idr"`
	AvgLat   float64 `json:"avg_latency_ms"`
	RTKSaved int64   `json:"rtk_saved"`
}

// Usage groups requests by client, model, upstream, preset, hour, minute or day.
func (s *Store) Usage(group string, since time.Time, client string) ([]UsageRow, error) {
	col := map[string]string{"client": "client", "model": "model", "upstream": "upstream", "preset": "preset",
		"hour":   "strftime('%Y-%m-%d %H:00', ts/1000, 'unixepoch')",
		"minute": "strftime('%Y-%m-%d %H:%M', ts/1000, 'unixepoch')",
		"day":    "strftime('%Y-%m-%d', ts/1000, 'unixepoch')"}[group]
	if col == "" {
		return nil, errors.New("bad group")
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT %s k, COUNT(*), COUNT(*)-SUM(ok), COALESCE(SUM(in_tok),0), COALESCE(SUM(out_tok),0), COALESCE(SUM(cost),0), COALESCE(AVG(latency_ms),0), COALESCE(SUM(rtk_saved),0) FROM requests WHERE ts>=? AND (?='' OR client=?) AND substr(client,1,1)!='_' GROUP BY k ORDER BY k`, col), since.UnixMilli(), client, client)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var u UsageRow
		var k sql.NullString
		if err := rows.Scan(&k, &u.Requests, &u.Errors, &u.InTok, &u.OutTok, &u.Cost, &u.AvgLat, &u.RTKSaved); err != nil {
			return nil, err
		}
		u.Key = k.String
		out = append(out, u)
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

// ---- JEV ----

// JEVScores returns stored judge scores.
func (s *Store) JEVScores() (map[string]JEVScore, error) {
	rows, err := s.db.Query(`SELECT model, score, n, ts FROM jev`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]JEVScore{}
	for rows.Next() {
		var m string
		var j JEVScore
		var ts int64
		if err := rows.Scan(&m, &j.Score, &j.N, &ts); err != nil {
			return nil, err
		}
		j.TS = time.Unix(ts, 0)
		out[m] = j
	}
	return out, rows.Err()
}

// SetJEV stores a judge result.
func (s *Store) SetJEV(model string, score float64, n int) {
	_, _ = s.db.Exec(`INSERT INTO jev(model,score,n,ts) VALUES(?,?,?,?) ON CONFLICT(model) DO UPDATE SET score=excluded.score, n=excluded.n, ts=excluded.ts`, model, score, n, time.Now().Unix())
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

// BenchRow is the leaderboard score of one catalog model.
type BenchRow struct {
	Value   float64 `json:"value"` // 0..1
	Rating  float64 `json:"rating"`
	Lo      float64 `json:"lo"`
	Hi      float64 `json:"hi"`
	Arena   string  `json:"arena"`
	Matched string  `json:"matched"`
	Date    string  `json:"date"`
	Votes   float64 `json:"votes"`
}

// ReplaceBench swaps the whole score table.
func (s *Store) ReplaceBench(rows map[string]BenchRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM bench`); err != nil {
		tx.Rollback()
		return err
	}
	for m, r := range rows {
		if _, err := tx.Exec(`INSERT INTO bench(model,value,rating,lo,hi,arena,matched,date,votes) VALUES(?,?,?,?,?,?,?,?,?)`, m, r.Value, r.Rating, r.Lo, r.Hi, r.Arena, r.Matched, r.Date, r.Votes); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Bench returns the stored leaderboard scores.
func (s *Store) Bench() (map[string]BenchRow, error) {
	rows, err := s.db.Query(`SELECT model,value,rating,lo,hi,arena,matched,date,votes FROM bench`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]BenchRow{}
	for rows.Next() {
		var m string
		var r BenchRow
		var matched, date, arena sql.NullString
		if err := rows.Scan(&m, &r.Value, &r.Rating, &r.Lo, &r.Hi, &arena, &matched, &date, &r.Votes); err != nil {
			return nil, err
		}
		r.Arena, r.Matched, r.Date = arena.String, matched.String, date.String
		out[m] = r
	}
	return out, rows.Err()
}
