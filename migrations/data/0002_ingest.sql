-- Every id is a UUIDv7, so ids reveal neither how many rows exist nor the
-- next one.

-- projects are the applications monitored by the instance.
CREATE TABLE projects (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

-- ingest_keys are the credentials of the SDKs of a Project. Only the SHA-256
-- of each key is stored. A key with revoked_at no longer authenticates.
CREATE TABLE ingest_keys (
	id         TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
	key        TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL,
	revoked_at TEXT
) STRICT;

-- issues group the equivalent Events of a Project by their Group Key, the
-- hex SHA-256 of its components.
CREATE TABLE issues (
	id             TEXT PRIMARY KEY,
	project_id     TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
	group_key      TEXT NOT NULL,
	status         TEXT NOT NULL CHECK (status IN ('open', 'resolved', 'ignored')),
	title          TEXT NOT NULL,
	culprit        TEXT NOT NULL,
	event_count    INTEGER NOT NULL,
	UNIQUE (project_id, group_key)
) STRICT;

-- events are the processed Events, stored once per Project and event_id.
CREATE TABLE events (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
	event_id    TEXT NOT NULL,
	issue_id    TEXT NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
	received_at TEXT NOT NULL,
	payload     BLOB NOT NULL,
	UNIQUE (project_id, event_id)
) STRICT;

CREATE INDEX events_issue_id ON events (issue_id);
