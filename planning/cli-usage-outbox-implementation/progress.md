# Progress

- 2026-08-13: Read repository instructions and TDD guidance.
- 2026-08-13: Confirmed isolated branch/worktree is clean.
- 2026-08-13: Started source and lifecycle inspection.
- 2026-08-13: Added durable outbox, lease/ack, write-failure, and slow-subscriber tests first.
- 2026-08-13: Verified RED with `go test ./internal/redisqueue`; build failed on the intentionally missing outbox API (`openOutbox`, `ConfigureOutbox`, `EnqueueWithError`, and `Status`).
- 2026-08-13: Implemented the SQLite outbox, stable delivery IDs, leases, selective idempotent ack, persisted counters, and legacy pop adapter.
- 2026-08-13: Added failing management/config tests, then implemented claim/ack/status routes and `usage-outbox-path` configuration.
- 2026-08-13: Added slow subscriber, disable retention, control-message exclusion, same-path reconfigure, and explicit write failure coverage.
- 2026-08-13: `go test ./...` passed across the repository.
- 2026-08-13: `go build -o test-output ./cmd/server && rm test-output` passed.
- 2026-08-13: `git diff --check` passed and the diff was reviewed for configuration lifecycle, API auth, raw payload compatibility, and secret leakage.
- 2026-08-13: Review found expired leases could be counted repeatedly during partial retry batches. Added a failing regression test, then atomically released expired leases before re-claim; focused tests passed.
- 2026-08-13: Review found a failed hot-reload path could close a working outbox. Added regression coverage and kept the existing durable backend active when the new target cannot be opened.
- 2026-08-13: A later fresh `go test ./...` hit an unrelated pre-existing flaky panic in `sdk/cliproxy.TestServiceInitialOverlayStagesPluginWritesUntilReady` (`close of closed channel`); the usage-outbox focused suites remained green. A prior full repository run completed successfully.
- 2026-08-13: Cross-component audit found the SDK usage dispatcher did not wait for queued plugin work during Stop, leaving a normal container-restart loss window ahead of the durable outbox.
- 2026-08-13: Added a failing shutdown-drain regression test, then made Stop wait for the dispatcher to finish queued plugin calls while preserving asynchronous request publication.
