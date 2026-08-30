# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)
- Production bind-mounts `config.yaml` as a single file. Do not atomically replace the host path while the container is running because the container remains attached to the old inode. Apply validated full-YAML changes through authenticated `PUT /v0/management/config.yaml`, verify a new successful reload log plus runtime state, and keep host/container config checksums aligned across restart.

## Production CPA Plugin Baseline
- Production uses the author's unmodified `Mxucc/cpa-account-config-manager` release artifacts; the previous custom fork line is retired.
- Current production baseline: verify the active upstream release at runtime; the
  2026-08 production baseline is `v0.3.1356` for Linux amd64.
- Production images that load the Linux `.so` plugin must be built with `CGO_ENABLED=1`; do not replace the image binary with a `CGO_ENABLED=0` cross-build. Verify the active binary build settings and plugin registration in an isolated candidate before rollout.
- Before deployment, verify the release archive against the author's published checksum and keep the active plugin binary plus its SHA256 in the rollback snapshot.
- After deployment, verify the loaded and registered plugin version, active plugin path, management UI availability, and the production container health checks.
- Future plugin upgrades should follow the author's upstream releases unless a later project decision explicitly restores a maintained custom fork.

## Production Container And Network Release Rules
- The final CPA container must be Compose-managed. Do not leave production on a
  manual `docker run` container after rollout; the canonical production Compose
  file must contain the active image, port, resource limits, networks, aliases,
  and stable host mounts.
- Auth material is authoritative under the stable host directory
  `/data/apps/cli-proxy-api/auths`. Never delete, rename, or garbage-collect a
  release directory while a running container still mounts it. Before cleanup,
  inspect `.Mounts`, verify the container auth-file count, and confirm the
  replacement container mounts stable `auths`, `plugins`, `data`, and `logs`.
- Keep exactly one authoritative `cli-proxy-api` alias on the shared
  `sub2api-access` network. Before switching it, verify DNS resolution from
  every caller (including NewAPI and Sub2API), remove stale containers that own
  the alias, and probe the new container directly and through the public Nginx
  path.
- Account-level IPv6 egress requires a post-recreate connectivity probe from
  the actual container namespace to an IPv6-only or dual-stack upstream. A
  route and assigned address are insufficient: verify TCP/HTTP success and
  `connection timed out=0`. Remove old containers before reusing their IPv6
  addresses so stale NDP/MAC mappings cannot black-hole the new container.
- Preserve `nodad` handling for allocated account IPv6 addresses and keep
  `CAP_NET_ADMIN`, `iproute2`, and the configured egress prefix in the final
  Compose definition. Do not test only on the host namespace.
- After rollout, compare current-window errors with cumulative log counters;
  historical timeout counts must not be reported as newly introduced failures.
- Release acceptance must include: active image/version and plugin registration,
  auth-file count, public root HTTP 200, direct container HTTP 200, caller DNS
  resolution, IPv6 probe, container `restart=0`/`OOMKilled=false`, and a short
  usage-event growth window.

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/runtime/executor/helps/codex_weekly_overdraft*.go` — Codex weekly-overdraft transform and process-local account observability. Keep per-auth data in memory only, expire after six hours of inactivity, and preserve separate observe/inject outcomes; do not persist auth IDs or merge action histories.
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Usage Stream Heartbeat Semantics
- The authenticated `usage` RESP subscription emits `{"heartbeat":true}` only while the usage queue is enabled.
- Heartbeats are source watermarks, not requests. They must never include auth identifiers, models, tokens, status codes, or cost and must not enter usage accounting.
- Keep the heartbeat interval bounded below the refill controller freshness window; disabling the usage queue must stop heartbeat output so consumers fail closed.

## Codex Usage Identity Semantics
- `chatgpt_account_id` identifies a shared ChatGPT workspace/Space, not an individual Business member. Never use it alone as a per-credential history key.
- Usage and management projections may expose only an opaque member fingerprint derived from `chatgpt_user_id`, with issuer-scoped `sub` as the compatibility fallback. Never expose the raw member ID.
- If the member fingerprint is unavailable, downstream usage systems must fall back to the exact auth file plus `auth_index`; they must not fall back to workspace-only aggregation.

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts

## Codex Availability Semantics
- Dynamic model-provider registrations remain authoritative when present.
- When a model exactly matches the static Codex catalog but every Codex credential is disabled, exhausted, or cooling down, keep the model routable to `codex` and let auth selection return HTTP 503. Do not collapse this temporary capacity state into HTTP 400 `model_not_found`.
- Truly unknown models must continue to return HTTP 400 and must not enter auth retry or failover paths.

## Responses Streaming Semantics
- HTTP SSE must emit a machine-readable terminal error event after headers are committed; never turn an upstream timeout, disconnect, quota failure, or credential failure into a clean EOF.
- WebSocket sessions may close silently for retryable upstream failures because reconnect is part of that transport contract. Do not reuse this WebSocket-only behavior for HTTP SSE.
- Preserve detailed messages for actionable request faults. Use generic status text for non-request failures so internal transport or credential details are not exposed downstream.
