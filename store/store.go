package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
)

const (
	ingestFile = "ingest.db"
	dataFile   = "data.db"
	lockFile   = ".lock"
	backupDir  = "backups"
	dirMode    = 0o750
	lockMode   = 0o600
	// pingQuery reads the schema, so it fails if the file is unreadable.
	pingQuery = "SELECT count(*) FROM sqlite_master"
	// readConns is the size of the read pool of each file.
	readConns = 8
)

var pragmas = []string{
	"busy_timeout = 10000",
	"journal_mode = WAL",
	"synchronous = NORMAL",
	"journal_size_limit = 200000000",
	"foreign_keys = ON",
	"temp_store = MEMORY",
	"cache_size = -32000",
}

var errLocked = errors.New("locked")

type Store struct {
	// Ingest is the queue of accepted Events.
	Ingest *DB
	// Data holds Projects, Issues, processed Events and Ingest Keys.
	Data *DB
	dir  string
	lock *os.File
}

type DB struct {
	Read  *sql.DB
	Write *sql.DB
}

type connector struct {
	path   string
	driver *sqlite.Driver
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	lock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}

	ingest, err := openDB(filepath.Join(dir, ingestFile))
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}

	data, err := openDB(filepath.Join(dir, dataFile))
	if err != nil {
		return nil, errors.Join(err, ingest.Close(), lock.Close())
	}

	return &Store{Ingest: ingest, Data: data, dir: dir, lock: lock}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return errors.Join(s.Ingest.Ping(ctx), s.Data.Ping(ctx))
}

func (s *Store) Close() error {
	return errors.Join(s.Ingest.Close(), s.Data.Close(), s.lock.Close())
}

// Ping reports an error unless both pools of the file answer a query that
// reads the file. A plain ping would not: the driver answers it without
// touching the file.
func (db *DB) Ping(ctx context.Context) error {
	return errors.Join(readSchema(ctx, db.Read), readSchema(ctx, db.Write))
}

func (db *DB) Close() error {
	return errors.Join(db.Read.Close(), db.Write.Close())
}

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", c.path, err)
	}

	for _, pragma := range pragmas {
		if _, err := conn.(driver.ExecerContext).ExecContext(ctx, "PRAGMA "+pragma, nil); err != nil {
			return nil, errors.Join(fmt.Errorf("apply pragma %s on %s: %w", pragma, c.path, err), conn.Close())
		}
	}

	return conn, nil
}

func (c connector) Driver() driver.Driver {
	return c.driver
}

func readSchema(ctx context.Context, pool *sql.DB) error {
	var tables int
	return pool.QueryRowContext(ctx, pingQuery).Scan(&tables)
}

func lockDir(dir string) (*os.File, error) {
	path := filepath.Join(dir, lockFile)
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockMode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := tryLock(lock); err != nil {
		if errors.Is(err, errLocked) {
			err = fmt.Errorf("data directory %s is in use by another process", dir)
		} else {
			err = fmt.Errorf("lock %s: %w", path, err)
		}
		return nil, errors.Join(err, lock.Close())
	}

	return lock, nil
}

func openDB(path string) (*DB, error) {
	c := connector{path: path, driver: &sqlite.Driver{}}

	write := sql.OpenDB(c)
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)

	read := sql.OpenDB(c)
	read.SetMaxOpenConns(readConns)
	read.SetMaxIdleConns(readConns)

	db := &DB{Read: read, Write: write}

	if err := write.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("open %s: %w", path, err), db.Close())
	}

	return db, nil
}
