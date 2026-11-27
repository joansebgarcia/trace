package store_test

import (
	"database/sql"
	"testing"

	"github.com/joansgarcia/trace/store"
)

func TestOpenAppliesPragmas(t *testing.T) {
	s := openStore(t, t.TempDir())

	want := map[string]string{
		"busy_timeout":       "10000",
		"journal_mode":       "wal",
		"synchronous":        "1",
		"journal_size_limit": "200000000",
		"foreign_keys":       "1",
		"temp_store":         "2",
		"cache_size":         "-32000",
	}

	for name, db := range map[string]*store.DB{"ingest": s.Ingest, "data": s.Data} {
		for pool, conn := range map[string]*sql.DB{"read": db.Read, "write": db.Write} {
			for pragma, value := range want {
				var got string
				if err := conn.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
					t.Fatalf("%s %s: read %s: %v", name, pool, pragma, err)
				}
				if got != value {
					t.Errorf("%s %s: %s = %s, want %s", name, pool, pragma, got, value)
				}
			}
		}
	}
}

func TestOpenSplitsReadAndWritePools(t *testing.T) {
	s := openStore(t, t.TempDir())

	for name, db := range map[string]*store.DB{"ingest": s.Ingest, "data": s.Data} {
		if got := db.Write.Stats().MaxOpenConnections; got != 1 {
			t.Errorf("%s write pool has %d connections, want 1", name, got)
		}
		if got := db.Read.Stats().MaxOpenConnections; got <= 1 {
			t.Errorf("%s read pool has %d connections, want more than 1", name, got)
		}
	}
}

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	return s
}
