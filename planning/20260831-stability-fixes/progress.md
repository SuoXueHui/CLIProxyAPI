# Progress

## 2026-08-31

- Read the updated project instructions and confirmed the request permits parallel sub-agents while forbidding online impact.
- Created this isolated planning directory.
- Created repair branch `codex/stability-fixes-20260831` from `master@20296cbd`.
- No production systems were contacted or modified.
- Next: dispatch independent bounded fixes.
- Weekly-overdraft review rejected a hard 25% core validation cap because it would break explicit configurations and pure transform callers. Kept the existing config contract; gate-state pruning remains the compatible local fix.
- User changed the delivery boundary: complete and merge locally, but do not publish anything until a later explicit message.
- Weekly-overdraft gate pruning followed RED/GREEN: the new cross-account expiry test initially failed to compile because no cleanup path existed, then passed after adding write-time pruning. Focused helper tests and the race run pass.
- Adaptive fixes completed through RED/GREEN for single/mixed plugin fail-open, programmatic disable, YAML/JSON round-trip, and optional config defaults; focused config/auth race tests pass.
- IPv6 fixes completed through fake-`ip` and controller tests covering bad-address repair, final-state verification, incremental collision state, deletion, prefix changes, disable, and failed removal. Main-agent review added a retained-reservation regression for healthy pre-existing addresses; egress and SDK race tests pass.
- Outbox fixes completed through RED/GREEN for explicit offline compaction, WAL busy reporting, status bytes, record/counter preservation, and disable races; redisqueue race tests pass.
- No production or remote publication action has been executed.
- Affected-package tests passed for config, auth, egress, SDK service, outbox, executor helpers, API/management, Responses, Codex auth, and proxy utilities.
- Race tests passed for config, auth, egress, SDK service, outbox, executor helpers, and executor packages.
- Required server build passed; `git diff --check` passed.
- Existing management package race remains unrelated to this change (`gin.SetMode` global state); it is not used as a blocking gate for the isolated outbox validation.
- Independent integration review of `bb575dc3` found startup-order, stale-config, partial-transition, and outbox maintenance gaps not modeled by the passing tests. Merge is paused while these are corrected.
- Integration corrections completed: startup reconcile, current-controller-only incremental updates, candidate egress transitions with config rollback, same-namespace address adoption, and startup-only outbox maintenance with physical sidecar accounting.
- Main-agent review removed the newly introduced single-controller `Configure` path so all config transitions must use the service two-phase transaction.
- Focused and race tests pass for egress, outbox, SDK service, config, auth, and weekly-overdraft helpers; required server build passes.
- A broad config-runtime serialization attempt failed the existing `TestConfigCommitDoesNotHoldCommitMutexDuringCooldownPersistence` contract and was replaced with a narrow egress generation check.
- Clean detached-worktree `go test ./... -count=1` passed at `8926566a`.
- Fresh race tests passed for config, adaptive auth, egress, outbox, weekly-overdraft helpers, and SDK service. The repository-wide executor race still has an unrelated existing Antigravity test-global race, so executor coverage remains focused on the modified helper package.
- Required server build and `git diff --check` pass. `go vet ./...` remains limited to the five previously documented upstream warnings in request logging and plugin callbacks.
- Fast-forward merged `codex/stability-fixes-20260831` into local `master`; no remote push, tag, image build, or online deployment was performed.
- Post-merge clean detached-worktree `go test ./... -count=1` passed on local `master`.
- Post-merge race tests passed for config, adaptive auth, egress, outbox, weekly-overdraft helpers, and SDK service; required build and diff checks passed.
- Task is complete in an unpublished state and waits for a later explicit release instruction.
