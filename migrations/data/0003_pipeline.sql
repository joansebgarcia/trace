-- The ingest pipeline: Redaction terms and rate limits of each Project, the
-- Group Key of each Event, and the lifecycle and aggregates of each Issue.
-- Times are UTC with nine fractional digits, so they sort as text.

-- redaction_terms is a JSON array of the key terms a Project adds to the
-- default ones. rate_limit, in Events/s, overrides the default rate limit;
-- NULL keeps the default.
ALTER TABLE projects ADD COLUMN redaction_terms TEXT NOT NULL DEFAULT '[]';
ALTER TABLE projects ADD COLUMN rate_limit REAL CHECK (rate_limit > 0);

-- grouping_version is the version of the Group Key algorithm that grouped
-- the Event; 0 is the provisional one before version 1. group_components is
-- the JSON array of the readable components behind its Group Key.
ALTER TABLE events ADD COLUMN grouping_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN group_components TEXT NOT NULL DEFAULT '[]';

-- level is the most severe level of the Issue's Events. first_seen and
-- last_seen are the received_at of its first and last Event.
-- status_changed_at is the time of the last change of status, automatic or
-- manual; resolved_at, of the last manual resolution; regressed_at, of the
-- last Regression. user_count is the number of rows of issue_users.
ALTER TABLE issues ADD COLUMN level TEXT NOT NULL DEFAULT 'error' CHECK (level IN ('fatal', 'error', 'warning'));
ALTER TABLE issues ADD COLUMN user_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE issues ADD COLUMN first_seen TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN last_seen TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN status_changed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN resolved_at TEXT;
ALTER TABLE issues ADD COLUMN regressed_at TEXT;

-- issue_users holds each distinct User of an Issue once.
CREATE TABLE issue_users (
	issue_id TEXT NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
	user_id  TEXT NOT NULL,
	PRIMARY KEY (issue_id, user_id)
) STRICT, WITHOUT ROWID;

-- Fill the new columns of the Issues stored before this migration.
INSERT OR IGNORE INTO issue_users (issue_id, user_id)
	SELECT issue_id, json_extract(CAST(payload AS TEXT), '$.user.id')
	FROM events
	WHERE json_extract(CAST(payload AS TEXT), '$.user.id') IS NOT NULL;

UPDATE issues SET
	first_seen = coalesce((SELECT min(received_at) FROM events WHERE issue_id = issues.id), ''),
	last_seen = coalesce((SELECT max(received_at) FROM events WHERE issue_id = issues.id), ''),
	user_count = (SELECT count(*) FROM issue_users WHERE issue_id = issues.id);
UPDATE issues SET status_changed_at = first_seen;
