# SQLite embedded, single process, two files with group commit

REC v0.1 stores everything in embedded SQLite (`modernc.org/sqlite`, WAL, `synchronous=NORMAL`) inside a single Go process, prioritising deployment simplicity (one binary, no database server) over the concurrent writers, `jsonb` and LISTEN/NOTIFY that Postgres would give. To make a single-writer engine fit ~500 Events/s, data is split into two files, each with its own single write connection: `ingest.db` (the queue of accepted Events) and `data.db` (Projects, Issues, processed Events, Ingest Keys). A dedicated writer goroutine group-commits accepted Events into `ingest.db` and only answers `202` after the commit; workers move them into `data.db` and delete them, so delivery is at-least-once and processing must be idempotent by `event_id`. SQLite and Go strategies (pragmas, read/write pool split, auxiliary file for high-churn data) follow PocketBase as a reference.

## Consequences

- Only one REC process per data directory; horizontal scaling is not possible without revisiting this decision.
- No atomic transaction spans both files; a crash between writing `data.db` and deleting from `ingest.db` causes reprocessing, not loss.
- Events from the last commits can be lost on power loss or OS crash, never on a REC process crash.
- Real-time notifications (SSE) are an in-process pub/sub fired after commit, not a database feature.
- Schema changes beyond simple `ALTER TABLE` need SQLite's table-rebuild procedure.
- The ~500 Events/s target is unverified on 2 vCPU / 4 GB; a benchmark is an acceptance criterion of the Fundaciones block and may reopen this ADR.
