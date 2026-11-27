package apis_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joansebgarcia/amber/migrations"
	"github.com/joansebgarcia/amber/store"
)

// The test sequences of data.db are made up. Those of ingest.db are the
// built-in ones, which the queue needs to start.
var (
	dataV1 = store.Migration{Version: 1, Name: "projects", SQL: "CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"}
	dataV2 = store.Migration{Version: 2, Name: "project_slug", SQL: "ALTER TABLE projects ADD COLUMN slug TEXT"}
	dataV3 = store.Migration{Version: 3, Name: "issues", SQL: "CREATE TABLE issues (id INTEGER PRIMARY KEY)"}
	ingest = loadIngest()
	// ingestOld is ingest without its last migration, as an earlier binary
	// would have it.
	ingestOld = ingest[:len(ingest)-1]
)

func TestStartMigratesNewDataDir(t *testing.T) {
	migrations := &store.Migrations{
		Ingest: ingest,
		Data:   []store.Migration{dataV1, dataV2, dataV3},
	}

	a, err := StartApp(t, t.TempDir(), WithMigrations(migrations))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}

	assertVersion(t, "ingest.db", a.Store().Ingest.Read, len(ingest))
	assertVersion(t, "data.db", a.Store().Data.Read, 3)
}

func TestStartAppliesBuiltInMigrations(t *testing.T) {
	a := NewApp(t)

	latest, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	assertVersion(t, "ingest.db", a.Store().Ingest.Read, len(latest.Ingest))
	assertVersion(t, "data.db", a.Store().Data.Read, len(latest.Data))
}

func TestStartMigratesOldDataSchema(t *testing.T) {
	dir := t.TempDir()
	migrateDir(t, dir, &store.Migrations{
		Ingest: ingest,
		Data:   []store.Migration{dataV1},
	}, "INSERT INTO projects (name) VALUES ('checkout')")

	a, err := StartApp(t, dir, WithMigrations(&store.Migrations{
		Ingest: ingest,
		Data:   []store.Migration{dataV1, dataV2, dataV3},
	}))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}

	assertVersion(t, "ingest.db", a.Store().Ingest.Read, len(ingest))
	assertVersion(t, "data.db", a.Store().Data.Read, 3)
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

func TestStartMigratesIngestWithoutDataBackup(t *testing.T) {
	dir := t.TempDir()
	migrateDir(t, dir, &store.Migrations{
		Ingest: ingestOld,
		Data:   []store.Migration{dataV1},
	})

	a, err := StartApp(t, dir, WithMigrations(&store.Migrations{
		Ingest: ingest,
		Data:   []store.Migration{dataV1},
	}))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}

	assertVersion(t, "ingest.db", a.Store().Ingest.Read, len(ingest))
	assertVersion(t, "data.db", a.Store().Data.Read, 1)
	assertBackups(t, dir)
}

func TestStartBacksUpNothingForNewDataDir(t *testing.T) {
	dir := t.TempDir()

	if _, err := StartApp(t, dir, WithMigrations(&store.Migrations{
		Ingest: ingest,
		Data:   []store.Migration{dataV1, dataV2},
	})); err != nil {
		t.Fatalf("start app: %v", err)
	}

	assertBackups(t, dir)
}

func TestStartRejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	migrateDir(t, dir, &store.Migrations{
		Ingest: ingest,
		Data:   []store.Migration{dataV1, dataV2, dataV3},
	})

	_, err := StartApp(t, dir, WithMigrations(&store.Migrations{
		Ingest: ingest,
		Data:   []store.Migration{dataV1, dataV2},
	}))

	want := "data.db schema version 3 is newer than this binary supports (2)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want one containing %q", err, want)
	}
}

func TestStartRejectsNewerSchemaWithoutChanges(t *testing.T) {
	dir := t.TempDir()
	migrateDir(t, dir, &store.Migrations{
		Ingest: ingestOld,
		Data:   []store.Migration{dataV1, dataV2, dataV3},
	})

	_, err := StartApp(t, dir, WithMigrations(&store.Migrations{
		Ingest: ingest,
		Data:   []store.Migration{dataV1, dataV2},
	}))
	if err == nil {
		t.Fatal("start app succeeded, want an error")
	}

	s := openDir(t, dir)
	assertVersion(t, "ingest.db", s.Ingest.Read, len(ingestOld))
}

func loadIngest() []store.Migration {
	schema, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		panic(err)
	}
	return schema.Ingest
}

// migrateDir leaves dir with its files migrated by migrations, as an earlier
// or later binary would, then runs statements on data.db.
func migrateDir(t *testing.T, dir string, migrations *store.Migrations, statements ...string) {
	t.Helper()

	s := openDir(t, dir)
	if err := s.Migrate(t.Context(), migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, statement := range statements {
		if _, err := s.Data.Write.Exec(statement); err != nil {
			t.Fatalf("exec %s: %v", statement, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

// openDir opens dir without starting an app. Closing the store again when the
// test ends is harmless.
func openDir(t *testing.T, dir string) *store.Store {
	t.Helper()

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
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
