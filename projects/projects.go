// Package projects resolves the Ingest Keys an SDK presents to their Project.
package projects

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/joansgarcia/trace/store"
)

// keySize is the number of random bytes of an Ingest Key.
const keySize = 32

// ErrKeyNotFound is returned by Revoke for a key that does not exist.
var ErrKeyNotFound = errors.New("ingest key not found")

// Project is an application monitored by the instance.
type Project struct {
	// ID is a UUIDv7.
	ID   string
	Name string
}

// Projects resolves Ingest Keys from an in-memory cache of the active keys in
// data.db. Every change to the keys must call Invalidate, so the next Resolve
// reloads the cache; between changes, Resolve never reads data.db.
type Projects struct {
	db *store.DB

	mu sync.RWMutex
	// keys maps the hash of each active key to its Project. It is nil while
	// the cache is invalid.
	keys map[string]Project
}

func New(db *store.DB) *Projects {
	return &Projects{db: db}
}

// Create adds a Project named name with a new Ingest Key, and returns both.
func (p *Projects) Create(ctx context.Context, name string) (Project, string, error) {
	key, err := newKey()
	if err != nil {
		return Project{}, "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := p.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, "", fmt.Errorf("begin create project: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	project := Project{ID: store.NewID(), Name: name}
	if _, err := tx.ExecContext(ctx, "INSERT INTO projects (id, name, created_at) VALUES (?, ?, ?)", project.ID, name, now); err != nil {
		return Project{}, "", fmt.Errorf("insert project: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO ingest_keys (id, project_id, key, created_at) VALUES (?, ?, ?, ?)", store.NewID(), project.ID, hashKey(key), now); err != nil {
		return Project{}, "", fmt.Errorf("insert ingest key: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Project{}, "", fmt.Errorf("commit create project: %w", err)
	}

	p.Invalidate()
	return project, key, nil
}

// Revoke makes key stop authenticating.
func (p *Projects) Revoke(ctx context.Context, key string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := p.db.Write.ExecContext(ctx, "UPDATE ingest_keys SET revoked_at = ? WHERE key = ? AND revoked_at IS NULL", now, hashKey(key))
	if err != nil {
		return fmt.Errorf("revoke ingest key: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return errors.Join(ErrKeyNotFound, err)
	}

	p.Invalidate()
	return nil
}

// Resolve returns the Project of key, and false if key is unknown or revoked.
// It reads data.db only to reload an invalid cache.
func (p *Projects) Resolve(ctx context.Context, key string) (Project, bool, error) {
	hash := hashKey(key)

	p.mu.RLock()
	keys := p.keys
	p.mu.RUnlock()
	if keys == nil {
		var err error
		if keys, err = p.load(ctx); err != nil {
			return Project{}, false, err
		}
	}

	project, ok := keys[hash]
	return project, ok, nil
}

// Invalidate makes the next Resolve reload the keys from data.db.
func (p *Projects) Invalidate() {
	p.mu.Lock()
	p.keys = nil
	p.mu.Unlock()
}

// load fills the cache unless another call already did. It holds the lock
// while it reads, so an Invalidate after a commit always discards a cache
// read before it.
func (p *Projects) load(ctx context.Context) (map[string]Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keys != nil {
		return p.keys, nil
	}

	rows, err := p.db.Read.QueryContext(ctx, `
		SELECT k.key, p.id, p.name
		FROM ingest_keys k JOIN projects p ON p.id = k.project_id
		WHERE k.revoked_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("load ingest keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	keys := map[string]Project{}
	for rows.Next() {
		var hash string
		var project Project
		if err := rows.Scan(&hash, &project.ID, &project.Name); err != nil {
			return nil, fmt.Errorf("load ingest keys: %w", err)
		}
		keys[hash] = project
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load ingest keys: %w", err)
	}

	p.keys = keys
	return keys, nil
}

func newKey() (string, error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate ingest key: %w", err)
	}
	return hex.EncodeToString(key), nil
}

// hashKey returns the hex SHA-256 of key, the form stored in data.db.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
