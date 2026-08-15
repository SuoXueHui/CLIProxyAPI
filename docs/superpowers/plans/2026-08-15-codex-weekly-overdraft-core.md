# Codex Weekly Overdraft Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a default-off, observable, stateless Codex weekly-overdraft transform to CPA core for HTTP and WebSocket requests.

**Architecture:** A validated nested Codex config controls a pure helper under `internal/runtime/executor/helps`. All Codex request transports call the helper at the same post-normalization boundary, while process-local atomic counters expose redacted status through one read-only management endpoint.

**Tech Stack:** Go 1.26+, `gjson`, `sjson`, Gin management API, Go atomics, YAML configuration.

**Spec:** `docs/superpowers/specs/2026-08-15-codex-weekly-overdraft-core-design.md`

## Global Constraints

- The feature is disabled by default.
- No synchronous probe, on-429 replay, scheduler change, token-drain routing, global request lock, or request-body persistence.
- Existing 401/402/403 hard-stop and 429 cooldown behavior remains unchanged.
- Request bodies, prompts, tokens, auth IDs, labels, and session IDs must not appear in new logs or management responses.
- Disabled and skipped transforms return the original body slice.

---

### Task 1: Configuration Contract

**Files:**
- Create: `internal/config/codex_overdraft.go`
- Create: `internal/config/codex_overdraft_test.go`
- Modify: `internal/config/config_types.go`
- Modify: `internal/config/config_load.go`
- Modify: `internal/config/parse.go`
- Modify: `internal/watcher/diff/config_diff.go`
- Modify: `internal/watcher/diff/config_diff_test.go`
- Modify: `sdk/config/config.go`
- Modify: `config.example.yaml`

**Interfaces:**
- Produces: `config.CodexWeeklyOverdraftConfig`, `DefaultCodexWeeklyOverdraftConfig()`, and `Validate() error`.
- Produces validated fields `Enabled`, `Mode`, `CanaryPercent`, `PairCount`, `TailPolicy`, `OAuthOnly`, and `MaxBodyBytes`.

- [x] Write failing tests for defaults, accepted values, rejected values, parsing parity, and config change details.
- [x] Run `go test ./internal/config ./internal/watcher/diff` and confirm the new tests fail for missing symbols/behavior.
- [x] Implement the config type, defaults, validation, public SDK aliases, example YAML, and redacted diff entries.
- [x] Run `gofmt -w` on changed Go files and rerun the focused tests until green.
- [x] Commit the independently passing configuration contract.

### Task 2: Pure Transform and Atomic Metrics

**Files:**
- Create: `internal/runtime/executor/helps/codex_weekly_overdraft.go`
- Create: `internal/runtime/executor/helps/codex_weekly_overdraft_test.go`
- Create: `internal/runtime/executor/helps/codex_weekly_overdraft_benchmark_test.go`

**Interfaces:**
- Consumes: `config.CodexWeeklyOverdraftConfig` from Task 1.
- Produces: `ApplyCodexWeeklyOverdraft(CodexWeeklyOverdraftRequest) ([]byte, CodexWeeklyOverdraftDecision)`.
- Produces: `RecordCodexWeeklyOverdraftOutcome(CodexWeeklyOverdraftDecision, error)` and `CodexWeeklyOverdraftStatusSnapshot()`.

- [x] Write table-driven failing tests for disabled, OAuth gating, malformed/oversized body, the three eligible tails, unsupported tails, stable canary, `1/2/4` pairs, and historical/core marker idempotency.
- [x] Add assertions that skipped results reuse the original backing slice and injected bodies contain matched call/output IDs.
- [x] Run `go test ./internal/runtime/executor/helps -run CodexWeeklyOverdraft` and confirm the expected failures.
- [x] Implement the stateless transform with stable SHA-256 bucketing, deterministic call IDs, one final JSON replacement, and atomic counters.
- [x] Add outcome classification tests for success, 429, 401/402/403, cancellation, and other errors.
- [x] Run focused tests and `go test -race ./internal/runtime/executor/helps -run CodexWeeklyOverdraft` until green.
- [x] Run the disabled/skip benchmarks and confirm zero replacement-body allocations.
- [x] Commit the independently passing transform and metrics.

### Task 3: HTTP and WebSocket Integration

**Files:**
- Modify: `internal/runtime/executor/codex_executor_execute.go`
- Modify: `internal/runtime/executor/codex_executor_stream.go`
- Modify: `internal/runtime/executor/codex_websockets_execute.go`
- Modify: `internal/runtime/executor/codex_websockets_stream.go`
- Create: `internal/runtime/executor/codex_weekly_overdraft_integration_test.go`

**Interfaces:**
- Consumes: Task 2 transform and decision/outcome APIs.
- Produces: identical transform behavior for HTTP non-streaming, HTTP streaming, WebSocket non-streaming, and WebSocket streaming paths.

- [x] Write failing HTTP tests that capture the upstream request and assert observe mode is unchanged while inject mode appends exactly one pair.
- [x] Write failing WebSocket tests that capture `response.create` and assert one pair remains one pair after a resend.
- [x] Run `go test ./internal/runtime/executor -run WeeklyOverdraft` and confirm the expected payload assertions fail.
- [x] Integrate the helper after reasoning replay and before cache/identity processing in all four paths.
- [x] Record non-stream outcomes on function return and stream outcomes at terminal event/error without changing retry ownership.
- [x] Run focused executor tests and `go test -race ./internal/runtime/executor -run WeeklyOverdraft` until green.
- [x] Commit the independently passing transport integration.

### Task 4: Read-Only Management Status

**Files:**
- Create: `internal/api/handlers/management/codex_weekly_overdraft.go`
- Create: `internal/api/handlers/management/codex_weekly_overdraft_test.go`
- Modify: `internal/api/server_management.go`

**Interfaces:**
- Consumes: effective config and Task 2 status snapshot.
- Produces: `GET /v0/management/codex-weekly-overdraft`.

- [x] Write a failing handler test for effective config, counters, and absence of credential/request fields.
- [x] Run `go test ./internal/api/handlers/management -run CodexWeeklyOverdraft` and confirm failure.
- [x] Implement the read-only handler and register the route.
- [x] Run focused management and API route tests until green.
- [x] Commit the independently passing status endpoint.

### Task 5: Final Verification and Rollout Notes

**Files:**
- Modify: `planning/20260815-codex-weekly-overdraft-core/task_plan.md`
- Modify: `planning/20260815-codex-weekly-overdraft-core/findings.md`
- Modify: `planning/20260815-codex-weekly-overdraft-core/progress.md`
- Modify if warranted: `/Users/suo/work/Obsidian/AI-Workspace/projects/CLIProxyAPI/07-问题排查.md`
- Modify if warranted: `AGENTS.md`

- [x] Run `gofmt -w` on every changed Go file.
- [x] Run all focused package tests.
- [x] Run `go test -race ./internal/runtime/executor/helps ./internal/runtime/executor`.
- [x] Run `go test ./...`.
- [x] Run `go build -o test-output ./cmd/server && rm test-output`.
- [x] Inspect `git diff --check`, `git status`, and the complete branch diff for secrets, body retention, duplicate injection, or scheduler/retry changes.
- [x] Update planning and durable project knowledge only with verified facts.
- [x] Commit final documentation/knowledge updates and report the branch, commits, verification, rollout order, and remaining experimental risk.
