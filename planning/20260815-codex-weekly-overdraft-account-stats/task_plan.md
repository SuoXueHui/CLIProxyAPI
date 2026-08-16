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
5. [completed] Review, merge both repositories to master, publish, and perform live read-only verification.
6. [completed] Check AGENTS.md and project knowledge synchronization.
7. [completed] Attempt the 25%/1-pair phase-two canary behind a stability gate and restore 10% when the first 11 injected outcomes were all hard stops.
8. [completed] Re-enable and hold 25%/1-pair at the user's direction so valid accounts can be imported, while gating only on CPA/Manager/Controller service health until useful account traffic exists.
9. [completed] After valid accounts were imported, observe account-level success/429/hard-stop results and decide to keep 25% because success recovered while credential reselection and 503 pressure remain material.
10. [pending] Reconcile stale active Codex auth files after the current 401 investigation; keep the freshly validated credential and prevent invalidated refresh tokens from remaining in the request pool.
11. [pending] Re-evaluate a 40% canary only after at least one additional stable observation window shows low credential reselection and sustained 503 below 1%; do not increase pair count at the same time.
12. [completed] User-directed 50% canary escalation: keep one pair and OAuth-only, verify the single-field diff, then observe post-change 401/429/503 and process health. The sustained window regressed materially, so 50% is not approved for continued stable operation.
13. [completed] Restore 25% under the existing stability-first authorization, verify the exact one-field diff, config convergence, hot reload, process health, and post-rollback request windows without restarting CPA.
14. [pending] Recover account capacity before any future canary increase: replenish usable OAuth accounts, reduce unavailable-credential reselection, and require a representative window with sustained HTTP 503 below 1%.

## Risks and boundaries
- `auth-id` is returned only from the authenticated management endpoint and is already a management-visible credential identifier.
- Per-auth outcomes must remain separated by observe/inject action so hot mode changes cannot relabel historical results.
- Account stats are operational evidence, not proof of upstream quota amount or extra token entitlement.
- Retention is inactivity TTL, not a persisted exact sliding-window ledger.
- Account tracking must be bounded and must never block or fail the upstream request path.
- During the pre-import hold, zero successful overdraft outcomes is not by itself a rollback trigger because the known account pool currently has OAuth failures; restart/OOM, endpoint health, config drift, reload failure, or new severe service errors remain rollback triggers.

## Errors
| Error | Attempt | Resolution |
|---|---:|---|
| Direct root `npx vitest` ignored the Manager web alias config and failed to resolve `@/` imports | 1 | Re-ran through `npm --workspace apps/web run test -- ...`; the focused baseline passed. |
| Package-wide management race test hit an existing `gin.SetMode` global-state race in unrelated parallel tests | 1 | Keep the passing full non-race suite, run the account tracker package with `-race`, and run the new management handler test alone with `-race`; do not expand this feature into unrelated test cleanup. |
| Repository-wide `go vet ./...` stopped on existing warnings in request logging and plugin callback context cleanup | 1 | Confirmed the feature branch does not modify those files; retain the warning as a baseline gate exception and continue with focused race plus compile verification. |
| The two-snapshot production summary exited 1 after printing both valid snapshots because the final loop's false `&& sleep` condition became the script exit status | 1 | Kept the captured JSON evidence and used an explicit final verification script whose last status is a real assertion, not a loop-control conditional. |
| The 25% hold release aborted before PUT because the strict diff whitelist did not account for timestamped `---`/`+++` file header lines | 1 | Confirmed no live mutation occurred, corrected only the whitelist expression, and retained the one-field config assertion. |
| The corrected 25% hold script still exited before PUT because `pipefail` treated an intentionally empty `grep -Ev` result as an error | 1 | Confirmed no live mutation occurred again, replaced the empty-result pipeline with an `awk` counter, and kept the candidate delta guard intact. |
| The first live account probe used the generic `/v0/management/accounts` path and received 404 | 1 | Read the deployed Manager router and used the actual `/v0/management/cpa-refill/accounts` route. |
| One log-context shell probe was truncated by nested local/remote quoting | 1 | Re-ran the same read-only inspection through an SSH heredoc; no production state changed. |
| User correlated the 401 with the 25% change | 1 | Compared live timestamps: `refresh_token_invalidated` began at 14:21:56 and the 10% -> 25% reload was 14:37:56; record 25% as exposure amplification, not token invalidation. |
| Invalid credentials remained `disabled=false` after OAuth failures | 1 | Read the live Manager policy endpoint and found both auth-issue queue and auto-disable disabled; CPA runtime quarantine is not persistent Manager disable. |
