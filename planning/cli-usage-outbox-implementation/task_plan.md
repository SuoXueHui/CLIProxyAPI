# CLI Usage Outbox Implementation Plan

## Goal
Implement a durable at-least-once usage outbox with claim/lease/ack management APIs while preserving the legacy destructive pop endpoint.

## Phases
- [complete] Inspect the current queue, configuration lifecycle, routes, and tests.
- [complete] Write failing durable outbox and queue behavior tests; verify RED.
- [complete] Implement the durable SQLite outbox and queue integration; verify GREEN.
- [complete] Write failing HTTP/config tests; verify RED.
- [complete] Implement management APIs, routing, and configuration; verify GREEN.
- [complete] Run formatting, focused tests, full tests, build, diff review, and commit.

## Constraints
- Work only in this isolated worktree.
- Tests precede production changes.
- Preserve raw usage payload JSON.
- Control messages must not enter durable storage.
- Disable and shutdown must retain persistent records.

## Errors
| Error | Attempt | Resolution |
|---|---:|---|
| Expected RED build failure for missing outbox API | 1 | Implemented the API after confirming the tests failed for the intended reason. |
| Full API suite initially saw stale global queue records | 1 | Default server initialization now configures a per-config durable SQLite outbox; rerun passed. |
| Fresh full suite hit unrelated `sdk/cliproxy` flaky panic (`close of closed channel`) | 1 | Recorded as unrelated; rerun the isolated package and complete build verification before final handoff. |
