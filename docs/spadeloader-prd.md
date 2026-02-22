# Spadeloader PRD (Server + CLI)

Status: Draft  
Owner: Spadeforge team  
Date: 2026-02-22

## 1. Summary

Spadeloader is a minimal remote FPGA flashing service and CLI.  
Its core function is to run:

```bash
openFPGALoader -b <board_name> <bitstream_path>
```

on the server after receiving:

1. FPGA board name
2. Human-readable design name
3. Bitstream file (`.bit`)

The system keeps a persistent history of the last 100 submitted designs in the data folder and supports zeroconf/mDNS discovery in the same style as Spadeforge.

## 2. Goals

1. Provide a simple, reliable API for remote bitstream flashing.
2. Provide a CLI that can run non-interactively (flags) and interactively (prompt for missing inputs).
3. Keep a durable history of the latest 100 designs.
4. Support automatic server discovery over mDNS when `--server` is not provided.
5. Reuse the proven Spadeforge architecture patterns: config/env, HTTP API, queue, persistence, discovery.

## 3. Non-Goals

1. Bitstream build/synthesis/place-and-route (Spadeloader only flashes provided bitstreams).
2. Multi-board orchestration/scheduling across multiple physical devices.
3. GUI/web dashboard.
4. Advanced programmer features outside `openFPGALoader -b <board> <bitstream>`.

## 4. Users and Core User Stories

1. As a developer, I can flash a local `.bit` file to a remote FPGA host with one command.
2. As a developer, I can omit `--server` and let the CLI discover the service on LAN.
3. As an operator, I can inspect job status/logs and diagnose failures.
4. As an operator, I can review recent designs flashed (up to last 100).

## 5. Functional Requirements

### FR-1: Flash submission input

Server accepts a flash job containing:

1. `board` (required, string)
2. `design_name` (required, string)
3. `bitstream` (required, file upload)

Validation:

1. `board` must match `^[A-Za-z0-9._-]{1,64}$`
2. `design_name` length 1..128 (trimmed)
3. `bitstream` extension must be `.bit`
4. `bitstream` size must be <= configured max upload bytes

### FR-2: Flash execution

Each accepted job is queued and processed by a single worker (one active flash at a time by default).  
Execution command must be created with `exec.CommandContext` (no shell interpolation):

```bash
openFPGALoader -b <board> <absolute_bitstream_path>
```

Terminal state is `SUCCEEDED` or `FAILED`, with captured exit code and log.

### FR-3: Status and logs

Expose per-job status endpoint and plain-text log retrieval endpoint.  
State machine mirrors Spadeforge style:

1. `QUEUED`
2. `RUNNING`
3. `SUCCEEDED`
4. `FAILED`

### FR-4: Persistent recent design history (last 100)

For every terminal job, append a history record and trim to last 100 entries (most recent first).  
History must survive restart and be stored under base data folder.

### FR-5: Zeroconf discovery

Server advertises mDNS service.  
CLI discovers it by default if `--server` is omitted.

## 6. Architecture (Simplified Spadeforge Pattern)

Proposed package layout:

1. `cmd/spadeloader` (server entrypoint)
2. `cmd/spadeloader-cli` (client entrypoint)
3. `internal/config` (env parsing + validation)
4. `internal/server` (HTTP routes + auth + allowlist guard)
5. `internal/client` (HTTP client + wait/poll)
6. `internal/queue` (persistent job queue + worker lifecycle)
7. `internal/flasher` (openFPGALoader executor)
8. `internal/store` (disk layout + state persistence)
9. `internal/history` (last-100 design ring persistence)
10. `internal/discovery` (mDNS browse/advertise; reused pattern)
11. `internal/job` (job record/state transitions)

Execution flow:

1. CLI submits multipart request.
2. Server validates fields and persists job + uploaded bitstream.
3. Queue worker starts job, runs `openFPGALoader`.
4. Server captures stdout/stderr to `console.log`.
5. Job marked terminal and saved.
6. History list updated and trimmed to 100.
7. CLI polls or streams status until terminal.

## 7. API Specification (v1)

### 7.1 Endpoints

1. `GET /healthz`
2. `POST /v1/jobs`
3. `GET /v1/jobs/{id}`
4. `GET /v1/jobs/{id}/log`
5. `GET /v1/designs/recent`

### 7.2 Submit job

`POST /v1/jobs`  
Content-Type: `multipart/form-data`

Fields:

1. `board` (text)
2. `design_name` (text)
3. `bitstream` (file)

Success response (`202 Accepted`):

```json
{
  "job_id": "d5af1c1b6d824f5f9df91c8f4ad2d57e",
  "state": "QUEUED"
}
```

Error response (`400/401/403/413`):

```json
{
  "error": "validation message"
}
```

### 7.3 Get job

`GET /v1/jobs/{id}` -> `200 OK`

```json
{
  "id": "d5af1c1b6d824f5f9df91c8f4ad2d57e",
  "state": "RUNNING",
  "message": "flashing",
  "error": "",
  "current_step": "flash",
  "created_at": "2026-02-22T18:02:11Z",
  "updated_at": "2026-02-22T18:02:17Z",
  "started_at": "2026-02-22T18:02:12Z",
  "finished_at": null,
  "exit_code": null,
  "board": "alchitry_au",
  "design_name": "Blink Demo v5",
  "bitstream_name": "design.bit",
  "bitstream_sha256": "..."
}
```

### 7.4 Get job log

`GET /v1/jobs/{id}/log` -> `text/plain`  
Contains combined stdout/stderr from `openFPGALoader`.

### 7.5 Recent designs

`GET /v1/designs/recent?limit=20` (default `20`, max `100`) -> `200 OK`

```json
{
  "items": [
    {
      "job_id": "d5af1c1b6d824f5f9df91c8f4ad2d57e",
      "design_name": "Blink Demo v5",
      "board": "alchitry_au",
      "bitstream_sha256": "...",
      "bitstream_size_bytes": 183452,
      "submitted_at": "2026-02-22T18:02:11Z",
      "finished_at": "2026-02-22T18:02:19Z",
      "state": "SUCCEEDED"
    }
  ]
}
```

## 8. CLI Specification

Command:

```bash
spadeloader-cli flash --board alchitry_au --name "Blink Demo v5" --bitstream design.bit
```

Alias:

```bash
spadeloader-cli --board ... --name ... --bitstream ...
```

Flags:

1. `--server` (optional; if empty, use discovery)
2. `--discover` (default `true`)
3. `--discover-timeout` (default `2s`)
4. `--discover-service` (default `_spadeloader._tcp`)
5. `--discover-domain` (default `local.`)
6. `--board` (required if non-interactive)
7. `--name` (required if non-interactive)
8. `--bitstream` (required if non-interactive)
9. `--token` and `--auth-header`
10. `--wait` (default `true`)
11. `--poll` (default `2s`)
12. `--show-log-on-fail` (default `true`)

Prompt behavior:

1. If stdin is a TTY and required fields are missing, prompt interactively:
   1. Board name
   2. Design name
   3. Bitstream path
2. If non-TTY and missing required fields, exit with usage error.

## 9. Data Storage and Persistence

Base folder: `SPADELOADER_BASE_DIR` (required)

Proposed layout:

1. `jobs/<job_id>/state.json`
2. `jobs/<job_id>/request.bit`
3. `artifacts/<job_id>/console.log`
4. `history/recent_designs.json`
5. `work/<job_id>/` (temporary runtime files)

History file contract (`history/recent_designs.json`):

```json
{
  "version": 1,
  "items": []
}
```

Retention algorithm:

1. Append new terminal entry.
2. Sort by `submitted_at` descending (or maintain append order newest-first).
3. Trim to 100.
4. Persist atomically (`write temp + rename`).

## 10. Zeroconf / mDNS Requirements

Server advertisement defaults:

1. Service: `_spadeloader._tcp`
2. Domain: `local.`
3. Instance: `spadeloader` (or hostname fallback)
4. TXT: `proto=http`, `path=/healthz`

Behavior parity with Spadeforge:

1. Disable advertisement when listening on loopback-only host.
2. Prefer eligible non-loopback interfaces.
3. Keep Tailscale filtering behavior as currently implemented in discovery module.

Client discovery behavior:

1. When `--server` absent and `--discover=true`, browse mDNS and pick first valid endpoint.
2. Discovered URL format: `http://<ip>:<port>`.

## 11. Configuration (Server)

Required:

1. `SPADELOADER_BASE_DIR`

Defaults:

1. `SPADELOADER_LISTEN_ADDR=:8080`
2. `SPADELOADER_OPENFPGALOADER_BIN=openFPGALoader`
3. `SPADELOADER_AUTH_HEADER=X-Build-Token`
4. `SPADELOADER_DISCOVERY_ENABLE=1`
5. `SPADELOADER_DISCOVERY_SERVICE=_spadeloader._tcp`
6. `SPADELOADER_DISCOVERY_DOMAIN=local.`
7. `SPADELOADER_DISCOVERY_INSTANCE=spadeloader`
8. `SPADELOADER_MAX_UPLOAD_BYTES=67108864` (64 MiB)
9. `SPADELOADER_WORKER_TIMEOUT=10m`
10. `SPADELOADER_HISTORY_LIMIT=100` (must not exceed 100 by product requirement)
11. `SPADELOADER_PRESERVE_WORK_DIR=0`

Optional hardening:

1. `SPADELOADER_TOKEN`
2. `SPADELOADER_ALLOWLIST` (CSV of IP/CIDR)

## 12. Security and Reliability

1. Use argument-safe process launch (`exec.CommandContext`), never shell command strings.
2. Validate all user inputs and upload size limits.
3. Write uploaded bitstream and state to disk before queueing.
4. Single-worker default to prevent concurrent device contention.
5. On server restart:
   1. Recover queued jobs.
   2. Requeue jobs that were in `RUNNING`.
6. Enforce worker timeout and process cancellation.
7. Store SHA-256 of bitstream for traceability.

## 13. Observability

1. Structured log lines at job lifecycle boundaries:
   1. submitted
   2. started
   3. progress step changes
   4. finished (with exit code)
2. `/healthz` returns service availability.
3. Per-job `console.log` is retrievable through API.

## 14. Test Plan

Unit tests:

1. Input validation for board/name/bitstream.
2. Job state transitions.
3. History trim logic (101+ entries -> 100).
4. Config/env parsing.
5. Discovery resolution and fallback behavior.

Integration tests:

1. API submit + poll terminal with fake flasher.
2. Timeout path and failed flash path.
3. Restart recovery for queued/running jobs.
4. CLI auto-discovery and explicit `--server` precedence.

E2E smoke test:

1. With real `openFPGALoader` and test board:
   1. Submit known-good bitstream.
   2. Verify terminal `SUCCEEDED`.
   3. Verify entry exists in recent history list.

## 15. Acceptance Criteria

1. User can submit board/name/bitstream and server executes `openFPGALoader -b <board> <bitstream>`.
2. CLI works with flags and interactive prompts for missing required values.
3. Last 100 designs are persisted under data folder and served via API.
4. mDNS discovery works by default and can be disabled/configured.
5. System behavior survives restart without losing persisted job/history metadata.
6. Tests cover core paths (success, validation failure, tool failure, timeout, recovery).

## 16. Implementation Notes (Migration from Spadeforge)

Reuse with minimal change:

1. `internal/discovery` (service default renamed)
2. `internal/config` pattern (env + validation)
3. `internal/server` guard pattern (token + allowlist)
4. `internal/queue` and `internal/job` state lifecycle
5. `internal/client` polling/discovery flow

Replace/remove:

1. Remove bundle/manifest/archive pipeline.
2. Replace `builder` with `flasher` that executes `openFPGALoader`.
3. Add `history` component for recent designs list.

This keeps implementation small while preserving reliability behaviors already validated in Spadeforge.
