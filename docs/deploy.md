# Deployment

This guide takes an Operator from nothing to a Trace instance in production. Trace is one binary with no database server and no configuration file: every setting is a flag of `trace serve` with a `TRACE_*` environment variable, and the flag wins over the variable.

## Requirements

- **A local disk for the data directory.** Trace keeps its data in SQLite files in WAL mode and locks the directory with `flock`. Network file systems (NFS, SMB/CIFS, EFS and the like) break both, and can corrupt the data. A block volume attached to the machine, such as EBS or a cloud persistent disk, is a local disk.
- **One Trace process per data directory.** A second `trace serve` on the same directory exits with `data directory ... is in use by another process`. Trace does not scale horizontally ([ADR 0001](adr/0001-sqlite-single-process.md)).
- **Linux, macOS or Windows** on amd64 or arm64, or Docker on linux/amd64 or linux/arm64.

### Capacity

The reference capacity is ~500 Events/s with an ingest p99 under 50 ms on 2 vCPU / 4 GB. The [benchmark](benchmark.md) has so far measured only the queue acceptance path, not the full ingest handler: 500 Events/s with a p99 of 14.68 ms, on a 16 GB macOS host with the server at `GOMAXPROCS=2`. A run on a real 2 vCPU / 4 GB machine is still pending.

## Settings

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `--http` | `TRACE_HTTP` | `127.0.0.1:8090` (`0.0.0.0:8090` in the image) | TCP address to listen on for HTTP |
| `--dir` | `TRACE_DIR` | `./trace_data` (`/data` in the image) | data directory, created if it does not exist |
| `--log-format` | `TRACE_LOG_FORMAT` | `text` | log format: `json` or `text` |
| `--log-level` | `TRACE_LOG_LEVEL` | `info` | minimum log level: `debug`, `info`, `warn` or `error` |
| `--shutdown-timeout` | `TRACE_SHUTDOWN_TIMEOUT` | `10s` | maximum time to wait for a graceful shutdown |
| `--trusted-proxies` | `TRACE_TRUSTED_PROXIES` | none | comma-separated CIDRs of the reverse proxies whose `X-Forwarded-For` and `X-Forwarded-Proto` headers are trusted |

`trace serve --help` lists the same flags. Logs go to stdout.

`GET /api/health` answers `200 {"status":"ok"}` when both databases respond and the writer is alive, and `503` otherwise. Point your orchestrator or load balancer at it. The image declares no `HEALTHCHECK`, because the distroless base has no shell or `curl` to run one.

## The data directory

```
<dir>/
├── .lock          held by the running process
├── ingest.db      queue of accepted Events (+ -wal, -shm)
├── data.db        processed data: Projects, Issues, Events (+ -wal, -shm)
└── backups/       pre-migrate-<from>-<to>.db snapshots of data.db
```

An Event is durable in `ingest.db` once Trace answers `202`. Background workers move it to `data.db`. On shutdown, Events still in `ingest.db` stay there and are processed on the next start, so both files matter for a backup.

## Docker

The image `ghcr.io/joansgarcia/trace` runs as the `nonroot` user (uid 65532) on a distroless base, and stores its data in the volume `/data`. Tags: `X.Y.Z` for each release, `X.Y` for the newest stable patch of that minor version, and `latest` for the newest stable release. Pre-releases publish only their `X.Y.Z` tag.

```sh
docker run -d --name trace \
  --restart unless-stopped \
  --stop-timeout 30 \
  -p 127.0.0.1:8090:8090 \
  -v trace_data:/data \
  ghcr.io/joansgarcia/trace:0.1
```

The port binds to `127.0.0.1` so that only a reverse proxy on the host reaches it; see [TLS with Caddy](#tls-with-caddy) for the `TRACE_TRUSTED_PROXIES` it needs. `--stop-timeout` gives Trace more time than its own `--shutdown-timeout` before Docker sends `SIGKILL`.

Use a named volume, or a host directory owned by uid 65532 (`sudo chown 65532:65532 /srv/trace`). A new named volume takes the right owner from the image.

### docker compose

```yaml
# docker-compose.yml
services:
  trace:
    image: ghcr.io/joansgarcia/trace:0.1
    restart: unless-stopped
    stop_grace_period: 30s
    ports:
      - "127.0.0.1:8090:8090"
    environment:
      TRACE_LOG_FORMAT: json
      # Only when a reverse proxy in front of Trace sets X-Forwarded-*.
      # TRACE_TRUSTED_PROXIES: 172.16.0.0/12
    volumes:
      - trace_data:/data

volumes:
  trace_data:
```

```sh
docker compose up -d
curl -fsS http://127.0.0.1:8090/api/health
```

The image already passes `serve --http=0.0.0.0:8090 --dir=/data`. To add flags, either set their `TRACE_*` variables, or replace the whole command, such as `command: ["serve", "--http=0.0.0.0:8090", "--dir=/data", "--log-level=debug"]`.

## Binary with systemd

Download the archive for your OS and architecture from the [releases](https://github.com/joansgarcia/trace/releases), and verify it against `checksums.txt`:

```sh
VERSION=0.1.0   # without the leading v
ARCH=amd64      # or arm64
BASE=https://github.com/joansgarcia/trace/releases/download/v$VERSION
curl -fsSLO "$BASE/trace_${VERSION}_linux_${ARCH}.tar.gz"
curl -fsSLO "$BASE/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "trace_${VERSION}_linux_${ARCH}.tar.gz" trace
sudo install -m 0755 trace /usr/local/bin/trace
trace --version
```

`trace --version` says whether the build has the web UI embedded; release builds do.

Create a dedicated system user with no login shell:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin trace
```

Install the unit as `/etc/systemd/system/trace.service`:

```ini
[Unit]
Description=Trace
After=network-online.target
Wants=network-online.target

[Service]
User=trace
Group=trace
# Creates /var/lib/trace owned by the trace user.
StateDirectory=trace
StateDirectoryMode=0750
ExecStart=/usr/local/bin/trace serve --dir=/var/lib/trace
Environment=TRACE_HTTP=127.0.0.1:8090
Environment=TRACE_LOG_FORMAT=json
Environment=TRACE_TRUSTED_PROXIES=127.0.0.1/32,::1/128
Restart=on-failure
# More than --shutdown-timeout, so systemd never kills a graceful shutdown.
TimeoutStopSec=30s

NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now trace
curl -fsS http://127.0.0.1:8090/api/health
journalctl -u trace -f
```

`ProtectSystem=strict` makes the file system read-only for Trace except for its `StateDirectory`.

## TLS with Caddy

Trace has no built-in TLS. Put it behind a reverse proxy and tell Trace which proxy to trust with `--trusted-proxies`. Without it, Trace ignores `X-Forwarded-For` and `X-Forwarded-Proto`, and logs the proxy's address as the client address.

With Caddy on the same host, this `Caddyfile` gets a certificate for the domain and forwards to Trace:

```
trace.example.com {
	reverse_proxy 127.0.0.1:8090
}
```

Caddy sets `X-Forwarded-For` and `X-Forwarded-Proto` by default. It connects from loopback, so trust loopback: `--trusted-proxies=127.0.0.1/32,::1/128`, as in the systemd unit above.

If Trace runs in Docker, it does not see loopback:

- With Caddy on the host and a published port (`-p 127.0.0.1:8090:8090`), Trace sees the gateway of its Docker network, such as `172.17.0.1` for the default bridge. Trust that address: `-e TRACE_TRUSTED_PROXIES=172.17.0.1/32`. `docker network inspect bridge` shows it.
- With Caddy in another container on the same network, Trace sees that container's address. Trust the subnet of the network, such as `172.16.0.0/12` for Docker's default address pools, and keep Trace's port off the public interface.

Trust only the addresses of your proxies. A trusted CIDR can set any client address in `X-Forwarded-For`.

## Backup and restore

### Offline copy

The simplest consistent backup is a copy of the stopped data directory:

```sh
sudo systemctl stop trace
sudo tar -czf trace-$(date +%F).tar.gz -C /var/lib/trace --exclude=.lock --exclude='.*-litestream' .
sudo systemctl start trace
```

Do not copy the `.db` files of a running instance with `cp` or `tar`: the copy can mix two moments and miss the WAL. For backups without downtime, use Litestream.

### Continuous replication with Litestream

[Litestream](https://litestream.io) replicates SQLite files to S3-compatible storage as they change, so a lost machine loses only the last seconds of data. Run it beside Trace on the same host and replicate both files:

```yaml
# /etc/litestream.yml
access-key-id: ${LITESTREAM_ACCESS_KEY_ID}
secret-access-key: ${LITESTREAM_SECRET_ACCESS_KEY}

dbs:
  - path: /var/lib/trace/data.db
    replica:
      url: s3://my-bucket/trace/data.db
  - path: /var/lib/trace/ingest.db
    replica:
      url: s3://my-bucket/trace/ingest.db
```

Litestream writes to the database files and keeps its state in `.data.db-litestream` and `.ingest.db-litestream` beside them, so run it as the `trace` user, such as with `User=trace` in its own systemd unit.

While Litestream holds a read transaction, Trace's final WAL checkpoint on shutdown can be blocked. `trace serve` then exits with an error, and systemd marks the unit as failed. The data stays intact: the WAL is still on disk, and the next start uses it.

To restore onto a new machine, restore both files into an empty data directory before Trace first starts, then start it:

```sh
sudo install -d -o trace -g trace -m 0750 /var/lib/trace
sudo -u trace litestream restore -config /etc/litestream.yml /var/lib/trace/data.db
sudo -u trace litestream restore -config /etc/litestream.yml /var/lib/trace/ingest.db
sudo systemctl start trace
```

`litestream restore` refuses to overwrite an existing file. To restore over an existing instance, stop Trace and move the old files, with their `-wal` and `-shm`, out of the way first.

### Restore from an offline copy

Stop Trace, replace the contents of the data directory with the copy, and start it:

```sh
sudo systemctl stop trace
sudo find /var/lib/trace -mindepth 1 -delete
sudo tar -xzf trace-2026-11-27.tar.gz -C /var/lib/trace
sudo chown -R trace:trace /var/lib/trace
sudo systemctl start trace
```

### The pre-migrate backups

Before a new version migrates the schema of `data.db`, Trace writes a consistent snapshot of it to `backups/pre-migrate-<from>-<to>.db`, where `<from>` and `<to>` are the schema versions. It is the way back if an upgrade goes wrong. It is not a substitute for regular backups: it exists only for migrations, and Trace never deletes old snapshots, so remove them once you trust the new version.

## Upgrade

Migrations run on start, before Trace listens, so an upgrade is: stop → replace the binary → start.

```sh
sudo systemctl stop trace
sudo install -m 0755 trace /usr/local/bin/trace   # the new version
sudo systemctl start trace
```

With Docker, pull the new tag and recreate the container on the same volume:

```sh
docker compose pull
docker compose up -d
```

On SIGTERM, Trace stops accepting requests, commits what it has accepted, and leaves unprocessed Events in `ingest.db` for the next start, within `--shutdown-timeout`. Nothing accepted is lost across the restart.

Trace refuses to start if a file's schema is newer than the binary (`... schema version N is newer than this binary supports`), so an accidental downgrade cannot damage the data. To roll back an upgrade that migrated `data.db`:

1. Stop Trace.
2. Move `data.db`, `data.db-wal` and `data.db-shm` aside.
3. Copy `backups/pre-migrate-<from>-<to>.db` to `data.db`.
4. Put the old binary back and start it.

Changes made after the upgrade are lost. If the upgrade also migrated `ingest.db`, the old binary refuses it; restore `ingest.db` from a backup taken before the upgrade. Taking an offline copy before each upgrade covers both files.
