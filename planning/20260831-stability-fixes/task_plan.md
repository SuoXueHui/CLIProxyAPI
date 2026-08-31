# CPA Stability Fixes

## Goal

Fix the confirmed stability defects from the seven-customization review on an isolated branch and merge the verified code back to local `master`, leaving it explicitly unpublished until the user sends a later release instruction.

## Safety Boundary

- No production SSH, Docker, Nginx, DNS, config, container, database, or credential changes.
- No remote push, release tag, production image, or release artifact publication in this task.
- Work only from the current local `master` snapshot and merge back locally after verification.
- Preserve unrelated untracked planning directories.

## Scope

- [complete] Create isolated branch and record baseline.
- [complete] Fix adaptive legacy/plugin fail-open ordering and programmatic disable semantics.
- [complete] Fix IPv6 allocator/address lifecycle and add deterministic ownership/self-heal tests.
- [complete] Add safe usage outbox maintenance/compaction handling without changing delivery semantics.
- [complete] Re-review weekly-overdraft gate behavior and decide the smallest safe code/config guard.
- [complete] Resolve integration-review findings for startup ordering, stale config updates, atomic egress transitions, and lossless outbox maintenance.
- [complete] Run focused/race/full tests, vet, build, merge local `master`, verify merged result, and stop before publication.

## Non-goals

- No production rollout or live configuration update.
- No remote push or release publication; wait for the user's later message.
- No broad refactor of the seven feature areas.
- No forced behavior change to an existing explicitly configured weekly-overdraft experiment without a proven compatibility path.

## Errors

| Error | Attempt | Resolution |
| --- | ---: | --- |
| None | 0 | N/A |
| Weekly-overdraft helper tests failed after a proposed 25% core validation cap | 1 | Rejected the incompatible cap; retained explicit config semantics and kept only gate-state pruning. |
| Package-wide management race run hit existing parallel `gin.SetMode` race | 1 | Verified affected outbox/API tests separately; keep the known unrelated race as a baseline exception. |
| First feature commit passed tests but independent review found lifecycle gaps | 1 | Keep it isolated on the repair branch, add RED lifecycle/failure tests, and do not merge until the review is clean. |
| Global config-runtime lock broke the non-blocking commit contract | 1 | Removed the broad lock and added an egress-only generation check immediately before runtime publication. |
| Global config-runtime lock broke the existing non-blocking commit contract | 1 | Removed the broad lock and added a generation check only around egress runtime publication. |
