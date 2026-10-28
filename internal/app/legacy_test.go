package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/store"
)

// What older versions kept in files is copied into the database once, and the
// files are left alone.
func TestImportLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "config-history"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "config-history", "20261026-144550.542.yaml"), []byte("a: 1\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "recovery-used.json"), []byte(`{"used":["abc"]}`), 0o600)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	importLegacyFiles(dir, st, t.Logf)
	if got := st.ListHistory(); len(got) != 1 || got[0] != "20261026-144550.542" {
		t.Fatalf("history not imported: %v", got)
	}
	if st.KVGet(auth.RecoveryKey) != `{"used":["abc"]}` {
		t.Fatal("used recovery codes must not become usable again")
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery-used.json")); err != nil {
		t.Fatal("the importer must leave the files alone")
	}
}
