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
- Fresh full `go test ./...` passed. Account tracker package race and the new management handler race test passed.
- Package-wide management race remains blocked by an unrelated existing `gin.SetMode` test race; repository-wide vet remains blocked by unchanged request logger/plugin callback warnings.
- Fresh compile verification and `git diff --check` passed. Manager account UI, embedded bundle, and all Manager frontend/backend gates also passed.
- Resumed the release session after the earlier compaction failure. CPA `master@d6753fd4` and Manager `master@c5a33a05` are clean and already match their fork `master` branches.
- Fresh read-only production baseline shows CPA still runs `cli-proxy-api:codex-weekly-overdraft-6e8229af-cgo-amd64` and Manager still runs `cpa-manager-plus:core-overdraft-ef4bbd92-amd64`; both have restart=0 and OOM=false, so the six-hour account extension is not deployed yet.
- Found the interrupted session's release directories and prebuilt CPA image. The CPA candidate log proves the amd64 image reports commit `d6753fd4` and loads/registers author plugin `v0.3.1332`; the copied checksum manifest still uses build-host absolute paths and must be repaired before relying on it.
- Repaired the release checksum manifests and validated both isolated candidates. Production CPA is now on `cli-proxy-api:codex-weekly-overdraft-accounts-d6753fd4-cgo-amd64` with restart=0/OOM=false; the first Manager switch auto-rolled back before page verification, leaving the older Manager healthy while release-check timing is diagnosed.
- Production Manager was successfully switched on the corrected second attempt to `cpa-manager-plus:core-overdraft-accounts-c5a33a05-amd64`; Controller remained on the same container ID and image.
- Final live verification at `2026-08-15T15:33:44Z`: CPA running restart=0/OOM=false, Manager running/healthy restart=0/OOM=false, root=200, unauthenticated models=401, Manager health/page=200, author plugin v0.3.1332 loaded and registered, selected severe logs absent, release checksums and rollback scripts valid.
- The live account snapshot has retention 21600 seconds, 2 tracked injected accounts, 2 injected requests, and 2 successful injected outcomes with no 429 or hard stop. Chrome shows the global CORE panel and per-account `CORE 6h` strips; two visible accounts each show `注入 1 / 成功 1`, and the browser console has no warning/error.
- Updated project AGENTS.md with the six-hour in-memory/action-separated contract and synchronized the CLIProxyAPI production change record in Obsidian.

## 2026-08-16
- Continued with a fresh read-only production check about 14 hours after deployment. CPA, Manager, and Controller remain running; CPA and Manager have restart=0/OOM=false, Manager is healthy, root/health/management endpoints return the expected 200/401 statuses, and selected panic/fatal/OOM logs remain absent.
- The live six-hour snapshot now contains 74 accounts and 923 injected requests, split into 540 success, 81 usage-limit, 298 hard-stop, 0 canceled, and 4 other-failure. The newest entry was active within seconds of the check, proving the account tracker is still advancing under real traffic.
- Chrome confirms the global CORE card and per-account `CORE 6h` strips render current injected/success/429/hard-stop values without overlap; the browser console has no warning/error.
- Reconfirmed the author plugin v0.3.1332 is enabled/registered for account management but its own weekly-overdraft experiment remains disabled, avoiding double injection.
- Final gate: three root/health/page checks returned 200, five authenticated CORE status samples returned 200 in 106--130 ms, and no CPA/Manager panic/fatal/OOM logs appeared. One unrelated monitoring analytics 500 was traced to a canceled projection query; CORE requests in the same interval stayed 200.
