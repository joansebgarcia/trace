# Benchmark

The benchmark verifies the assumption of [ADR 0001](adr/0001-sqlite-single-process.md): ~500 Events/s with a p99 ingest latency under 50 ms on 2 vCPU / 4 GB. It is an acceptance criterion, not a CI test.

## How it runs

`make bench` builds `bin/trace` as `make go-build` does, creates a Project in a temporary data directory, starts `trace serve` on a free port over it, and runs the load generator in [`bench/`](../bench) against it.

- The load goes to the real ingest endpoint, `POST /api/ingest/v1/events`, with the Project's Ingest Key: one Event of the protocol v1 per request, about 2 KiB, with a new `event_id` each, frames, Breadcrumbs, and values that Redaction replaces. Each Event goes through the whole path: validation, Redaction, rate limit, backpressure, the group commit to `ingest.db`, and the workers that group it into its Issue in `data.db`. The latency is that of the `202`, which arrives after the group commit.
- The server runs with `--ingest-rate-limit` at twice the load, so the limiter runs on every Event without refusing any.
- The load is open-loop: Events go out at a fixed rate (500/s by default) whatever the server does, and each latency counts from the moment its request was due. A slow server cannot slow the load and hide its own latency.
- A run passes with no failed Events, at least 95% of the target rate accepted, and p99 under the target. Otherwise the generator exits with status 1.
- `BENCH_SERVER` wraps the server binary, such as `BENCH_SERVER="env GOMAXPROCS=2"`. `BENCH_FLAGS` passes flags to the generator (`go run ./bench -h`), such as `BENCH_FLAGS="-rate 1000 -duration 60s"`.

The `bench` workflow (`workflow_dispatch`) runs `make bench` on a GitHub runner inside a systemd scope of 2 vCPU and 4 GB with no swap, and publishes the report in the run summary and as an artifact. The load generator shares the scope with the server, which makes the result pessimistic.

The generator runs on Unix only: it starts the server in a process group of its own and stops it with SIGTERM, then SIGKILL after 30 s.

If a run on 2 vCPU / 4 GB misses the target, reopen ADR 0001 instead of tuning silently.

## Results

| Date | Path | Host | Accepted | p50 | p99 | Max | Verdict |
|---|---|---|---|---|---|---|---|
| 2026-11-26 | queue acceptance | macOS, i9-9880H, 16 GB; server `GOMAXPROCS=2` | 15000 in 30 s (500.0 Events/s) | 3.79 ms | 14.68 ms | 68.97 ms | Pass |
| 2026-11-27 | ingest endpoint | macOS, i9-9880H, 16 GB; server `GOMAXPROCS=2` | 15000 in 30 s (499.9 Events/s) | 4.95 ms | 10.4 ms | 58.8 ms | Pass |
