# Zero-Impact CPA Release Control Implementation Plan

## Task 1: Lifecycle State And Admission

- Add lifecycle modes, status snapshots, generation/CAS transitions, request admission, and active request counting.
- Add authenticated GET/PUT lifecycle management endpoints through an API-level interface to avoid package cycles.
- Cover standby/read-only/active/draining route behavior and transition serialization.

## Task 2: Credential Writer Fencing

- Add manager write/refresh/preparation gates with backward-compatible active defaults.
- Add file-store read-only enforcement, including load-time repair suppression.
- Add plugin host auth-write gating.
- Cover 401 refresh, request preparation, Save/Delete, and plugin callback behavior.

## Task 3: Service Lifecycle Integration

- Parse instance lifecycle environment settings during service construction.
- Acquire/release the writer lease on supported platforms.
- Gate plugin/auth/file-store components before plugin bootstrap.
- Skip IPv6 and auto-refresh outside active mode.
- Implement promote, drain, and demote sequencing with rollback.

## Task 4: Router And Release Assets

- Add reviewed Compose and Nginx templates plus release scripts for router install, backend reload, caller DNAT handoff, validation, and rollback.
- Use distinct projects/services/ports and stable mounts.

## Task 5: Verification And Integration

- Run focused unit/race tests, full `go test ./... -count=1`, required build, and diff check in the isolated worktree.
- Review the complete diff for scope and backward compatibility.
- Commit, fast-forward merge to local master, rebuild the production candidate, and repeat isolated smoke validation before any cutover.
