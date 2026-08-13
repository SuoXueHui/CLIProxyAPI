# Request Monitoring Release Plan (2026-08-13)

## Goal

Validate the already implemented durable request-monitoring delivery repair, integrate the authoritative master revisions, and deploy both CLIProxyAPI and CPA Manager to production with rollback evidence.

## Scope and guardrails

- Work only from isolated worktrees; preserve the dirty primary workspace.
- Root cause is the volatile/destructive usage-event delivery path, not the cost formula.
- Keep request handling asynchronous; Manager acknowledges only after SQLite commit.
- Back up live compose/config/inspect state before replacement.
- Do not delete the durable outbox on rollback.
- Validate canary accounting before broader acceptance.

## Phases

- [completed] 1. Recover prior investigation, implementation, and release state.
- [completed] 2. Re-review branch/master topology and run fresh repository verification.
- [completed] 3. Prepare production artifacts and read-only live baseline.
- [completed] 4. Deploy CLIProxyAPI and CPA Manager with rollback snapshots.
- [completed] 5. Run synthetic/end-to-end reconciliation and observe health.
- [completed] 6. Update project knowledge and publish final evidence.

## Errors

| Error | Attempt | Resolution |
|---|---:|---|
| Initial live SSH password attempt was rejected | 1 | Treat the stored credential as stale; check current SSH/session routes and continue local verification while recovering the active access path. |
