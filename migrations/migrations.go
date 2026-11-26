// Package migrations holds the schema migrations built into the binary: one
// directory per SQLite file, named after it without the extension, with
// migrations named "<version>_<name>.sql", such as "0002_issues.sql".
package migrations

import "embed"

// Files holds the "ingest" and "data" directories.
//
//go:embed ingest/*.sql data/*.sql
var Files embed.FS
