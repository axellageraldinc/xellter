package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/axellageraldinc/xellter/internal/auth"
	"github.com/axellageraldinc/xellter/internal/store"
)

// importLegacyFiles copies what older versions kept in files under data/ into
// the database: the catalog cache, the login counters, the used recovery codes
// and the config history. It only fills what the database does not hold yet and
// never touches the files, so it is safe on every start.
func importLegacyFiles(dir string, st *store.Store, logf func(string, ...any)) {
	for key, name := range map[string]string{
		"catalog.snapshot": "catalog.json",
		auth.StateKey:      "login-state.json",
		auth.RecoveryKey:   "recovery-used.json",
	} {
		if st.KVGet(key) != "" {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil && len(b) > 0 {
			st.KVSet(key, string(b))
			logf("imported %s into the database", name)
		}
	}
	if len(st.ListHistory()) > 0 {
		return
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "config-history"))
	for _, e := range ents {
		if b, err := os.ReadFile(filepath.Join(dir, "config-history", e.Name())); err == nil {
			st.AddHistory(strings.TrimSuffix(e.Name(), ".yaml"), b)
		}
	}
	if len(ents) > 0 {
		logf("imported %d config versions into the database", len(ents))
	}
}
