# Findings

- Maintained `master` is clean at `90ae75df` and matches the live production image tag `cli-proxy-api:refill-heartbeat-90ae75df-amd64`.
- The feature branch is clean at `8d1bb6f0`, based on current `origin/main` at `78f0c407`.
- `master` and the feature branch share upstream `v7.2.131` commit `323b7276`; merging brings 13 upstream fixes plus the nine feature commits while preserving 30 production-master commits.
- Production host is `174.128.243.42`, Compose workdir `/data/apps/cli-proxy-api`, file `docker-compose.prod.yml`.
- Live container is running with restart=0, OOM=false, root HTTP 200, unauthenticated models 401, about 3.0 GiB memory at baseline.
- Live plugin settings currently have author `weekly_overdraft_enabled=true` and adaptive variants disabled; the new core config is not present.
- Merged `master@6e8229af` passed fresh focused race tests, an uncached full `go test -count=1 ./...`, the required server build, `gofmt`, and `git diff --check`, then was pushed to `fork/master`.
- The first incremental candidate exposed a release-process bug rather than a feature bug: a locally cross-built `CGO_ENABLED=0` binary cannot load the production Linux `.so` plugin. The service was restored to the previous image and settings before rebuilding.
- The working production pattern is the repository Dockerfile with `CGO_ENABLED=1`. The corrected image was validated in an isolated container with the author plugin loaded, no plugin-load failure, the core status endpoint enabled, and the plugin overdraft switch disabled.
- Production now runs `cli-proxy-api:codex-weekly-overdraft-6e8229af-cgo-amd64`; the author plugin file and SHA256 are unchanged at `v0.3.1332`.
- Effective core canary config is `enabled=true`, `mode=inject`, `canary-percent=10`, `pair-count=1`, `tail-policy=user-and-tool-output`, `oauth-only=true`, and `max-body-bytes=8388608`. Both plugin config and persisted plugin settings have `weekly_overdraft_enabled=false`; adaptive weekly overdraft remains disabled.
- A real `gpt-5.4` Responses smoke completed with HTTP 200. The final live snapshot reached `evaluated=169`, `injected=9`, `non-canary=80`, `unsupported-tail=80`, `success=7`, and `other-failure=1`, proving the production core transform is active and multiple injected requests completed successfully.
- Final release artifacts and rollback snapshots are under `/data/apps/cli-proxy-api/releases/codex-weekly-overdraft-6e8229af-20260815T074704Z/`.
