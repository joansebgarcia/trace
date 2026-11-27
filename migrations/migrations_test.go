package migrations_test

import (
	"testing"

	"github.com/joansgarcia/trace/migrations"
	"github.com/joansgarcia/trace/store"
)

func TestBuiltInMigrationsApplyToANewDataDir(t *testing.T) {
	schema, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Migrate(t.Context(), schema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for name, want := range map[string]int{"ingest.db": len(schema.Ingest), "data.db": len(schema.Data)} {
		db := s.Ingest.Read
		if name == "data.db" {
			db = s.Data.Read
		}
		var got int
		if err := db.QueryRow("PRAGMA user_version").Scan(&got); err != nil {
			t.Fatalf("read %s version: %v", name, err)
		}
		if got != want {
			t.Errorf("%s version = %d, want %d", name, got, want)
		}
	}
}
