# Progress

- 2026-08-30: Created branch `codex/adaptive-auth-scheduler` from `master`.
- 2026-08-30: Confirmed clean baseline except for unrelated untracked planning directories.
- 2026-08-30: Design approved by user: soft slow-auth avoidance plus non-blocking fair scheduling; total ingress concurrency must not be reduced.
- 2026-08-30: Implementation started on the dedicated branch; configuration and scheduler tests are being added before production code.
- 2026-08-30: Added opt-in routing configuration with defaults and validation.
- 2026-08-30: Added in-memory adaptive scheduler state, soft penalty fail-open behavior, virtual load scoring, mixed-provider selection, and plugin candidate filtering.
- 2026-08-30: Added stream and non-stream in-flight leases with first-event and completion observations.
- 2026-08-30: Full regression excluding the pre-existing untracked-artifact `internal/util` invariant passed; race tests, vet, and server build passed.
- 2026-08-30: Merged to `master` as `b6eac33c`, tagged `v7.2.147`, and pushed to the production fork.
- 2026-08-30: Built production amd64 candidate on `174.128.243.42` as `cli-proxy-api:v7.2.147-adaptive-b6eac33c-amd64`; candidate plugin v0.3.1356 loaded and root endpoint returned 200.
- 2026-08-30: Enabled `routing.adaptive-auth` through an authenticated candidate management `PUT /v0/management/config.yaml`, preserving a config backup.
- 2026-08-30: Switched Nginx from host port 18376 to 18378 and cut over Sub2API alias to the independently managed `rollout-adaptive` container; initial health checks passed.
- 2026-08-30: Started one-hour rollback monitor `/tmp/cpa-adaptive-monitor.sh`; first checks show running/restart=0/OOM=false, direct/public/Sub2API HTTP 200, DAD=0, and no new Nginx upstream errors.
- 2026-08-30: One-hour observation completed without rollback. Candidate remained running/restart=0/OOM=false, direct/public/Sub2API checks stayed HTTP 200, DAD stayed 0, and no new Nginx upstream errors were observed.
- 2026-08-30: Final 60-minute account 23027 sample: 791 requests, average duration 14.69s, P50 7.53s, P95 59.16s, average first token 3.76s, first token >=30s count 1, duration >=120s count 11.
- 2026-08-30: Final production image is `cli-proxy-api:v7.2.147-adaptive-b6eac33c-amd64`; Nginx points to 18378 and Sub2API resolves `cli-proxy-api` to the adaptive container. Config backup and candidate assets remain under `/data/apps/cli-proxy-api/rollout`.
