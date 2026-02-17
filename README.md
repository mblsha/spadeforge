# spadeforge

Minimal remote Vivado build service for Spade bundles.

## What it does

- Accepts a zipped job bundle (`manifest.json` + HDL + XDC)
- Spools jobs to disk and runs one worker sequentially
- Executes Vivado in batch Tcl non-project mode
- Exposes current step + heartbeat while jobs are running
- Exposes REST endpoints for status, logs, and artifacts
- Supports mDNS auto-discovery (`_spadeforge._tcp`) so clients can find the server without `--server`

## Layout

- `cmd/spadeforge`: server binary
- `cmd/spadeforge-cli`: client submission binary
- `internal/server`: HTTP API
- `internal/queue`: persistent queue manager + worker lifecycle
- `internal/builder`: `FakeBuilder` and `VivadoBuilder`
- `internal/client`: Linux wrapper helpers (bundle + HTTP client)

## API

- `GET /healthz`
- `POST /v1/jobs` (`multipart/form-data`, file field `bundle`)
- `GET /v1/jobs/{id}`
- `GET /v1/jobs/{id}/artifacts`
- `GET /v1/jobs/{id}/log`

`GET /v1/jobs/{id}` includes `current_step` and `heartbeat_at` while running.

## Server config (env)

- `SPADEFORGE_BASE_DIR` (required)
- `SPADEFORGE_LISTEN_ADDR` (default `:8080`)
- `SPADEFORGE_TOKEN` (optional)
- `SPADEFORGE_AUTH_HEADER` (default `X-Build-Token`)
- `SPADEFORGE_ALLOWLIST` (optional CSV of IP/CIDR)
- `SPADEFORGE_VIVADO_BIN` (default `vivado`)
- `SPADEFORGE_MAX_UPLOAD_BYTES`
- `SPADEFORGE_MAX_EXTRACTED_FILES`
- `SPADEFORGE_MAX_EXTRACTED_TOTAL_BYTES`
- `SPADEFORGE_MAX_EXTRACTED_FILE_BYTES`
- `SPADEFORGE_WORKER_TIMEOUT`
- `SPADEFORGE_RETENTION_DAYS`
- `SPADEFORGE_USE_FAKE_BUILDER=1` (dry-run mode)
- `SPADEFORGE_PRESERVE_WORK_DIR=1` (keep per-job work dirs for debugging; default removes them)
- `SPADEFORGE_DISCOVERY_ENABLE=0` (disable mDNS advertisement)
- `SPADEFORGE_DISCOVERY_SERVICE` (default `_spadeforge._tcp`)
- `SPADEFORGE_DISCOVERY_DOMAIN` (default `local.`)
- `SPADEFORGE_DISCOVERY_INSTANCE` (default `spadeforge`)

## Example

Run server:

```bash
SPADEFORGE_BASE_DIR=/tmp/spadeforge SPADEFORGE_USE_FAKE_BUILDER=1 spadeforge
```

Submit from Linux side:

```bash
spadeforge-cli \
  --top top \
  --part xc7a35tcsg324-1 \
  --source build/spade.sv \
  --xdc constraints/top.xdc \
  --output-dir output
```

This creates extracted artifacts under `output/<job_id>/`. Use `--out-zip <path>` to also keep the raw zip.
By default the CLI auto-discovers the server via mDNS when `--server` is not set.

## Tests

- Unit and integration tests run without Vivado.
- Real Vivado smoke test is behind build tag `vivado` and requires env vars:
  - `VIVADO_BIN`
  - `VIVADO_PART`

Command (when Go toolchain is installed):

```bash
go test ./...
go test -tags vivado -timeout 90m ./internal/server -run TestVivadoSmoke_ServerPipeline
```
