# Trace
It is an open-source, platform-agnostic engine for real-time error capture, grouping, and diagnosis, designed to be self-hosted on any server or cloud environment without heavy dependencies.

## Deployment

Trace ships as one binary, `trace`, for Linux, macOS and Windows on amd64 and arm64, and as the image `ghcr.io/joansebgarcia/trace`. It needs no database server and no configuration file:

```sh
docker run -d -p 127.0.0.1:8090:8090 -v trace_data:/data ghcr.io/joansebgarcia/trace:0.1
```

The [deployment guide](docs/deploy.md) covers Docker, systemd, TLS with Caddy, backups, upgrades and the reference capacity.

## Public contract

Trace follows [semantic versioning](https://semver.org). Its public contract is only:

- **the ingest protocol**: the HTTP API through which SDKs send Events to a Trace server;
- **the binary**: the `trace` commands, their flags and `TRACE_*` environment variables, the data directory it reads, and the image's volume, port and defaults.

A breaking change to either needs a new major version. The Go packages in this repository are not part of the contract: they are the implementation of the binary, and they can change in any release. Do not import them.
