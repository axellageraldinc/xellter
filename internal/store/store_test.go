package store

import (
	"database/sql"
	"path/filepath"
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
	rows, err := st.Usage("preset", time.Now().Add(-time.Hour), "")
	if err != nil || len(rows) != 1 || rows[0].Key != "text" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

// The canary flag and the first-seen dates are gone: an old database is cleaned
// up on open, and a new row is written without them.
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
	// a request is still logged after the cleanup
	st.Log(Request{TS: time.Now().UnixMilli(), Client: "c", Preset: "text", Model: "m", OK: true})
	st.Flush()
	rows, err := st.Recent(10, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
