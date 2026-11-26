package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Migration is one forward step of the schema of a file. Version n is
// applied on top of version n-1; version 0 is an empty file.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrations holds the independent sequences of the two files, each ordered
// by version from 1.
type Migrations struct {
	Ingest []Migration
	Data   []Migration
}

// plan is the pending work on one file.
type plan struct {
	name       string
	db         *DB
	migrations []Migration
	// backup, if not nil, runs before changing a file that is not new.
	backup func(ctx context.Context, from, to int) error
	from   int
}

// LoadMigrations reads migrations from fsys, which holds an "ingest" and a
// "data" directory with files named "<version>_<name>.sql".
func LoadMigrations(fsys fs.FS) (*Migrations, error) {
	ingest, err := loadSequence(fsys, "ingest")
	if err != nil {
		return nil, err
	}
	data, err := loadSequence(fsys, "data")
	if err != nil {
		return nil, err
	}
	return &Migrations{Ingest: ingest, Data: data}, nil
}

// Migrate applies the pending migrations of both files, each in a single
// transaction. Before migrating data.db from a version other than 0, it
// snapshots the file to backups/pre-migrate-<from>-<to>.db. It changes
// nothing if a sequence is invalid or a file is newer than its sequence.
func (s *Store) Migrate(ctx context.Context, migrations *Migrations) error {
	plans := []plan{
		{name: ingestFile, db: s.Ingest, migrations: migrations.Ingest},
		{name: dataFile, db: s.Data, migrations: migrations.Data, backup: s.backupData},
	}

	for i := range plans {
		if err := plans[i].check(ctx); err != nil {
			return err
		}
	}
	for _, p := range plans {
		if err := p.apply(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) backupData(ctx context.Context, from, to int) error {
	dir := filepath.Join(s.dir, backupDir)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create backups directory: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("pre-migrate-%d-%d.db", from, to))
	// VACUUM INTO refuses an existing file, and a crash mid-copy must not
	// leave a partial backup under the final name.
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove stale backup: %w", err)
	}
	if _, err := s.Data.Write.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		return fmt.Errorf("back up %s: %w", dataFile, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("back up %s: %w", dataFile, err)
	}

	return nil
}

// check validates the sequence and reads the current version of the file,
// failing if the file is newer than the sequence.
func (p *plan) check(ctx context.Context) error {
	for i, m := range p.migrations {
		if m.Version != i+1 {
			return fmt.Errorf("%s migration %q has version %d, want %d", p.name, m.Name, m.Version, i+1)
		}
	}

	from, err := readVersion(ctx, p.db.Write)
	if err != nil {
		return fmt.Errorf("read %s schema version: %w", p.name, err)
	}
	if to := len(p.migrations); from > to {
		return fmt.Errorf("%s schema version %d is newer than this binary supports (%d): run a newer amber", p.name, from, to)
	}
	p.from = from

	return nil
}

func (p *plan) apply(ctx context.Context) error {
	to := len(p.migrations)
	if p.from == to {
		return nil
	}
	if p.backup != nil && p.from > 0 {
		if err := p.backup(ctx, p.from, to); err != nil {
			return err
		}
	}

	tx, err := p.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", p.name, err)
	}
	for _, m := range p.migrations[p.from:] {
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return errors.Join(fmt.Errorf("migrate %s to version %d (%s): %w", p.name, m.Version, m.Name, err), tx.Rollback())
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", to)); err != nil {
		return errors.Join(fmt.Errorf("set %s schema version: %w", p.name, err), tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate %s: %w", p.name, err)
	}

	return nil
}

func readVersion(ctx context.Context, pool *sql.DB) (int, error) {
	var version int
	err := pool.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}

func loadSequence(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	var migrations []Migration
	for _, entry := range entries {
		base, ok := strings.CutSuffix(entry.Name(), ".sql")
		if !ok {
			continue
		}
		file := path.Join(dir, entry.Name())
		number, name, _ := strings.Cut(base, "_")
		version, err := strconv.Atoi(number)
		if err != nil {
			return nil, fmt.Errorf("migration %s has no version prefix", file)
		}
		query, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, fmt.Errorf("read migration: %w", err)
		}
		migrations = append(migrations, Migration{Version: version, Name: name, SQL: string(query)})
	}

	return migrations, nil
}
