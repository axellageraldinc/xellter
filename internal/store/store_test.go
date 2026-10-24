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
	st.Log(Request{TS: time.Now().UnixMilli(), Client: "c", Preset: "balanced", Model: "m", OK: true})
	st.Flush()
	rows, err := st.Usage("preset", time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 1 || rows[0].Key != "balanced" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
