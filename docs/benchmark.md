# Benchmark

The benchmark verifies the assumption of [ADR 0001](adr/0001-sqlite-single-process.md): ~500 Events/s with a p99 ingest latency under 50 ms on 2 vCPU / 4 GB. It is an acceptance criterion, not a CI test.

## How it runs

`make bench` builds `bin/trace-bench` with the `bench` build tag, starts `trace serve` on a free port over a temporary data directory, and runs the load generator in [`bench/`](../bench) against it.

- The `bench` tag adds `POST /api/bench/events`, which enqueues the request body as one Event and answers `202` after the group commit to `ingest.db`. Release builds (`make build`) leave the tag out, so the endpoint does not exist there.
- The load is open-loop: Events go out at a fixed rate (500/s by default) whatever the server does, and each latency counts from the moment its request was due. A slow server cannot slow the load and hide its own latency.
- A run passes with no failed Events, at least 95% of the target rate accepted, and p99 under the target. Otherwise the generator exits with status 1.
- `BENCH_SERVER` wraps the server binary, such as `BENCH_SERVER="env GOMAXPROCS=2"`. `BENCH_FLAGS` passes flags to the generator (`go run ./bench -h`), such as `BENCH_FLAGS="-rate 1000 -duration 60s"`.

The `bench` workflow (`workflow_dispatch`) runs `make bench` on a GitHub runner inside a systemd scope of 2 vCPU and 4 GB with no swap, and publishes the report in the run summary and as an artifact. The load generator shares the scope with the server, which makes the result pessimistic.

The generator runs on Unix only: it starts the server in a process group of its own and stops it with SIGTERM, then SIGKILL after 30 s.

Until the ingest endpoint arrives with the Pipeline block, the benchmark measures only the queue acceptance path. Repeat it against the full handler (validation and Redaction) when it does.

If a run on 2 vCPU / 4 GB misses the target, reopen ADR 0001 instead of tuning silently.

## Results

| Date | Path | Host | Accepted | p50 | p99 | Max | Verdict |
|---|---|---|---|---|---|---|---|
| 2026-11-26 | queue acceptance | macOS, i9-9880H, 16 GB; server `GOMAXPROCS=2` | 15000 in 30 s (500.0 Events/s) | 3.79 ms | 14.68 ms | 68.97 ms | Pass |
