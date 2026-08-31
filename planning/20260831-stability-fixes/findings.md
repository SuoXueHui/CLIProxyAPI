# Findings

## Baseline

- Starting branch: local `master`.
- Current repair branch: `codex/stability-fixes-20260831`, based on `master@20296cbd`.
- Existing untracked planning directories are unrelated and must remain untouched.

## Confirmed Defects To Fix

1. Adaptive legacy/plugin selection filters soft penalties before hard availability, allowing `auth_unavailable` when a soft-penalized auth is the only hard-available candidate.
2. Programmatic `AdaptiveAuthConfig{Enabled:false}` is treated as omitted because the explicit flag is only populated by YAML decoding.
3. IPv6 `EnsureAddress` treats existing `tentative/dadfailed` addresses as success; allocator collision state is recreated per update batch; `nodad` does not solve simultaneous blue/green ownership.
4. Usage outbox ACK deletes rows but has no bounded compaction/maintenance strategy for high-water SQLite files.
5. Weekly-overdraft code is tested and defaults off, but `gate_mode=off` intentionally opens injection without evidence; historical high canaries regressed production request quality. Core validation must preserve explicit configurations; high-canary rollout policy belongs in the Manager/release gate, while this branch may safely bound process-local gate state.

## Verification Requirements

- Every fix needs a focused regression test that fails against the baseline or a deterministic invariant test.
- Run affected package tests, race tests, clean-tree full tests, required build, and post-merge verification.

## Implemented Design

### Adaptive Auth

- Legacy/plugin and mixed legacy paths now compute hard availability before soft-penalty filtering, preserving fail-open and preventing avoidable `auth_unavailable` responses.
- File parsing explicitly starts from `DefaultAdaptiveAuthConfig()`, while `WithDefaults()` no longer rewrites `Enabled`; programmatic false can therefore remain false without changing omitted-file defaults.
- YAML/JSON serialization emits `enabled` so a disabled programmatic config survives round-trip.

### IPv6 Egress

- `EnsureAddress` inspects the target address, repairs `tentative/dadfailed` state with `replace ... nodad`, and verifies the final state. If replace cannot clear bad flags, it deletes/re-adds and verifies again.
- A long-lived service controller preserves allocator collision state across incremental batches, reconciles config changes, updates runtime auth bindings, and removes only addresses attached by the current process.
- Healthy addresses discovered after process restart remain externally owned and are not deleted. Their allocator reservation is retained after auth deletion so the still-present address cannot be reassigned to a colliding auth.
- Cross-container ownership remains a deployment invariant: old containers must release the prefix before a replacement starts using the same stable addresses.

### Usage Outbox

- Status now exposes physical SQLite main/WAL/SHM storage and reclaimable main-database bytes.
- `MaintainOutboxAtStartup(ctx)` runs before usage publishing is enabled. It applies the 64 MiB/50% reclaim threshold, runs `VACUUM`, verifies/truncates the WAL checkpoint, and preserves pending/inflight records and counters.
- Durable and memory enqueue paths recheck queue enablement inside their serialization lock so a disable cannot admit or publish a late event.
- No HTTP/management compaction route exists; runtime callers cannot pause a live queue for maintenance.

### Weekly Overdraft

- Expired gate evidence is pruned across accounts when verified evidence is written, bounding stale process-local state without a background worker.
- Existing `gate_mode=off` and canary configuration semantics remain compatible. High-canary safety remains a Manager/release policy, not a core parser restriction.

## Remaining Boundaries

- IPv6 cannot enforce exclusive ownership between containers from one process. Release procedures must remove the old container before reusing stable account addresses.
- Outbox compaction remains startup-only and may extend startup time when a large file crosses the maintenance threshold.
- No production system was contacted during implementation.

## Independent Integration Review

- Existing file/OAuth auths are loaded before the API starts, but their IPv6 bindings are not synchronously reconciled before traffic can arrive.
- Auth update batches can capture an old config and later call the controller after a new config was committed, potentially reverting the egress prefix.
- The first controller reconfiguration design deletes old addresses before every new address/runtime binding is known to succeed, so a mid-transition failure can leave mixed config/network/runtime state.
- Healthy addresses discovered after restart are reserved but not process-owned; later disable/prefix changes need an explicit adoption/reconciliation policy to avoid stale addresses without deleting unrelated addresses.
- The first outbox compaction API requires queue disable, causing producers to silently skip usage during a long vacuum. It also has no built-in executable maintenance workflow and its size fields omit WAL/SHM physical bytes.
- These findings block local master merge despite passing tests; new lifecycle tests and design corrections are required.

## Integration Corrections

- Startup now reconciles loaded file/OAuth auths before the API server is created, so the first request cannot observe an empty or stale egress binding.
- Incremental auth add/modify/delete operations serialize address ownership with runtime registration/removal under the egress mutex and never carry a config snapshot into the controller.
- Config changes stage a candidate controller and every target runtime binding before publication. Candidate failure restores the previous config and leaves the old controller/bindings active; successful publication cleans retired addresses while retaining any IP reused by the candidate.
- Existing healthy addresses in the same network namespace are adopted by the active controller so delete/disable/prefix changes can clean them. Cross-container exclusion remains an explicit release rule.
- Outbox compaction was moved to `MaintainOutboxAtStartup`: no online disable window exists. The service invokes it after durable storage opens and before the API enables usage publishing. Default maintenance requires at least 64 MiB reclaimable and 50% free pages.
- Outbox storage status and maintenance results now use physical main database, WAL, and SHM sizes; reclaimable bytes remain the SQLite freelist estimate.
- The runtime `CompactOutbox` entry was removed, so online callers cannot accidentally discard usage during maintenance.

## Final State

- Local `master` contains the verified fixes and remains ahead of `fork/master`; nothing was pushed or published.
- The repair branch points to the same commit as local `master` and is retained for traceability.
- Clean detached-worktree full tests and focused race tests passed on the merged result.
