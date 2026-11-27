package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joansgarcia/trace/store"
)

// The test sequences are made up.
var (
	ingestV1 = store.Migration{Version: 1, Name: "init", SQL: "CREATE TABLE queue (id INTEGER PRIMARY KEY)"}
	ingestV2 = store.Migration{Version: 2, Name: "payload", SQL: "ALTER TABLE queue ADD COLUMN payload BLOB"}
	dataV1   = store.Migration{Version: 1, Name: "projects", SQL: "CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"}
	dataV2   = store.Migration{Version: 2, Name: "project_slug", SQL: "ALTER TABLE projects ADD COLUMN slug TEXT"}
	dataV3   = store.Migration{Version: 3, Name: "issues", SQL: "CREATE TABLE issues (id INTEGER PRIMARY KEY)"}
)

func TestMigrateNewDataDir(t *testing.T) {
	s := openStore(t, t.TempDir())

	migrate(t, s, &store.Migrations{
		Ingest: []store.Migration{ingestV1, ingestV2},
		Data:   []store.Migration{dataV1, dataV2, dataV3},
	})

	assertVersion(t, "ingest.db", s.Ingest.Read, 2)
	assertVersion(t, "data.db", s.Data.Read, 3)
}

func TestMigrateOldDataSchema(t *testing.T) {
	dir := t.TempDir()
	migrateDir(t, dir, &store.Migrations{
		Ingest: []store.Migration{ingestV1},
		Data:   []store.Migration{dataV1},
	}, "INSERT INTO projects (name) VALUES ('checkout')")
	s := openStore(t, dir)

	migrate(t, s, &store.Migrations{
		Ingest: []store.Migration{ingestV1},
		Data:   []store.Migration{dataV1, dataV2, dataV3},
	})

	assertVersion(t, "ingest.db", s.Ingest.Read, 1)
	assertVersion(t, "data.db", s.Data.Read, 3)
	assertBackups(t, dir, "pre-migrate-1-3.db")

	backup, err := sql.Open("sqlite", filepath.Join(dir, "backups", "pre-migrate-1-3.db"))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = backup.Close() }()
	assertVersion(t, "backup", backup, 1)
	var name string
	if err := backup.QueryRow("SELECT name FROM projects").Scan(&name); err != nil || name != "checkout" {
		t.Errorf("backup project = %q, %v; want checkout", name, err)
	}
}

func TestMigrateIngestWithoutDataBackup(t *testing.T) {
	dir := t.TempDir()
	migrateDir(t, dir, &store.Migrations{
		Ingest: []store.Migration{ingestV1},
		Data:   []store.Migration{dataV1},
	})
	s := openStore(t, dir)

	migrate(t, s, &store.Migrations{
		Ingest: []store.Migration{ingestV1, ingestV2},
		Data:   []store.Migration{dataV1},
	})

	assertVersion(t, "ingest.db", s.Ingest.Read, 2)
	assertVersion(t, "data.db", s.Data.Read, 1)
	assertBackups(t, dir)
}

func TestMigrateBacksUpNothingForNewDataDir(t *testing.T) {
	dir := t.TempDir()

	migrate(t, openStore(t, dir), &store.Migrations{
		Ingest: []store.Migration{ingestV1},
		Data:   []store.Migration{dataV1, dataV2},
	})

	assertBackups(t, dir)
}

func TestMigrateRejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	migrateDir(t, dir, &store.Migrations{
		Ingest: []store.Migration{ingestV1},
		Data:   []store.Migration{dataV1, dataV2, dataV3},
	})

	err := openStore(t, dir).Migrate(t.Context(), &store.Migrations{
		Ingest: []store.Migration{ingestV1},
		Data:   []store.Migration{dataV1, dataV2},
	})

	want := "data.db schema version 3 is newer than this binary supports (2)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want one containing %q", err, want)
	}
}

func TestMigrateRejectsNewerSchemaWithoutChanges(t *testing.T) {
	dir := t.TempDir()
	migrateDir(t, dir, &store.Migrations{
		Ingest: []store.Migration{ingestV1},
		Data:   []store.Migration{dataV1, dataV2, dataV3},
	})
	s := openStore(t, dir)

	err := s.Migrate(t.Context(), &store.Migrations{
		Ingest: []store.Migration{ingestV1, ingestV2},
		Data:   []store.Migration{dataV1, dataV2},
	})
	if err == nil {
		t.Fatal("migrate succeeded, want an error")
	}

	assertVersion(t, "ingest.db", s.Ingest.Read, 1)
}

func migrate(t *testing.T, s *store.Store, migrations *store.Migrations) {
	t.Helper()

	if err := s.Migrate(t.Context(), migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

// migrateDir leaves dir with its files migrated by migrations, as an earlier
// or later binary would, then runs statements on data.db.
func migrateDir(t *testing.T, dir string, migrations *store.Migrations, statements ...string) {
	t.Helper()

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	migrate(t, s, migrations)
	for _, statement := range statements {
		if _, err := s.Data.Write.Exec(statement); err != nil {
			t.Fatalf("exec %s: %v", statement, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

func assertVersion(t *testing.T, name string, db *sql.DB, want int) {
	t.Helper()

	var got int
	if err := db.QueryRow("PRAGMA user_version").Scan(&got); err != nil {
		t.Fatalf("read %s version: %v", name, err)
	}
	if got != want {
		t.Errorf("%s version = %d, want %d", name, got, want)
	}
}

// assertBackups fails the test unless the backups of dir are exactly want.
func assertBackups(t *testing.T, dir string, want ...string) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read backups: %v", err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("backups = %v, want %v", got, want)
	}
}
