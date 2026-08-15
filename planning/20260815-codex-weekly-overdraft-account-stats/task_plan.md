# Codex Weekly Overdraft Account Statistics

## Goal
Expose per-auth Codex weekly-overdraft activity for the most recent six hours without persistence or request-path serialization, then surface it in the Manager account list.

## Approved scope
- Keep the existing global process counters and endpoint backward compatible.
- Add per-auth observe/inject action counters and terminal outcomes in process memory only.
- Expire entries after six hours of inactivity; CPA restart clears all entries.
- Use concurrent lookup plus atomic counters; no global lock, request replay, database write, or body retention.
- Manager joins CPA `auth-id` to Controller `cpa_auth_id` and aggregates merged credentials.
- Render a compact `CORE 6h` strip inside the existing usage-window cell.

## Phases
1. [completed] Create the isolated feature branch and verify the clean baseline.
2. [completed] TDD the CPA per-auth in-memory tracker, retention, filtering, and management contract.
3. [completed] TDD the Manager DTO, account join/aggregation, compact UI, i18n, and embedded bundle.
4. [completed] Run focused, full, race, type, lint, build, and embedded-asset verification.
5. [in_progress] Review, merge both repositories to master, publish, and perform live read-only verification.
6. [pending] Check AGENTS.md and project knowledge synchronization.

## Risks and boundaries
- `auth-id` is returned only from the authenticated management endpoint and is already a management-visible credential identifier.
- Per-auth outcomes must remain separated by observe/inject action so hot mode changes cannot relabel historical results.
- Account stats are operational evidence, not proof of upstream quota amount or extra token entitlement.
- Retention is inactivity TTL, not a persisted exact sliding-window ledger.
- Account tracking must be bounded and must never block or fail the upstream request path.

## Errors
| Error | Attempt | Resolution |
|---|---:|---|
| Direct root `npx vitest` ignored the Manager web alias config and failed to resolve `@/` imports | 1 | Re-ran through `npm --workspace apps/web run test -- ...`; the focused baseline passed. |
| Package-wide management race test hit an existing `gin.SetMode` global-state race in unrelated parallel tests | 1 | Keep the passing full non-race suite, run the account tracker package with `-race`, and run the new management handler test alone with `-race`; do not expand this feature into unrelated test cleanup. |
| Repository-wide `go vet ./...` stopped on existing warnings in request logging and plugin callback context cleanup | 1 | Confirmed the feature branch does not modify those files; retain the warning as a baseline gate exception and continue with focused race plus compile verification. |
