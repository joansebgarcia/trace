package store

import "github.com/google/uuid"

// NewID returns a new id for a row of data.db: a UUIDv7, so ids reveal
// neither how many rows exist nor the next one. It panics only if the system
// has no source of randomness.
func NewID() string {
	return uuid.Must(uuid.NewV7()).String()
}
