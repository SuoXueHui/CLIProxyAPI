# Progress

## 2026-08-15
- User approved an adjusted phase-one design: account-level details, process memory only, six-hour inactivity retention, no persistence.
- Reused the clean isolated CPA worktree and created branch `codex/codex-weekly-overdraft-account-stats-20260815` from deployed core master.
- Confirmed Manager can join CPA `auth-id` directly to Controller `cpa_auth_id`, including grouped credential summaries.
- Clean CPA baseline `go test ./...` passed.
- Focused Manager baseline passed: 3 files / 26 tests. The first direct Vitest invocation was invalid because it bypassed the web alias config; the corrected workspace command passed.
- Added CPA failing tests for action-separated per-auth counters, six-hour inactivity expiry, auth-ID filtering, concurrent updates, and the management response contract.
- Confirmed the expected RED state: the account DTO, retention field, filterable snapshot, and per-auth counters did not exist.
- Implemented a bounded `sync.Map`/atomic tracker, action-scoped outcomes, six-hour expiry, deterministic snapshots, and optional management query filtering.
- Focused tests and the account tracker race test pass.
