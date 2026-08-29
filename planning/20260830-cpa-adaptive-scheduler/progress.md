# Progress

- 2026-08-30: Created branch `codex/adaptive-auth-scheduler` from `master`.
- 2026-08-30: Confirmed clean baseline except for unrelated untracked planning directories.
- 2026-08-30: Design approved by user: soft slow-auth avoidance plus non-blocking fair scheduling; total ingress concurrency must not be reduced.
- 2026-08-30: Implementation started on the dedicated branch; configuration and scheduler tests are being added before production code.
- 2026-08-30: Added opt-in routing configuration with defaults and validation.
- 2026-08-30: Added in-memory adaptive scheduler state, soft penalty fail-open behavior, virtual load scoring, mixed-provider selection, and plugin candidate filtering.
- 2026-08-30: Added stream and non-stream in-flight leases with first-event and completion observations.
