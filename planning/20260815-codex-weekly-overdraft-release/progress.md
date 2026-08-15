# Progress

## 2026-08-15
- Confirmed both the maintained master worktree and feature worktree are clean.
- Read-only production baseline completed over authorized SSH; no services or files changed.
- Established release scope, rollback requirements, and canary sequence.
- Started a non-fast-forward merge of the verified feature branch into maintained `master`.
- Resolved the only merge conflict by retaining both independent management route tests; focused quota and weekly-overdraft API tests pass.
- Fresh merged-master verification passed: focused weekly-overdraft race tests, uncached full tests, required server build, formatting, and diff checks.
- Pushed `master@6e8229af` to the maintained fork and uploaded reproducible source/binary artifacts plus rollback snapshots to production.
- The first isolated candidate validated the new core endpoint, but the first production switch revealed that the local cross-build used `CGO_ENABLED=0`; the author `.so` plugin failed to load. Restored the previous image, Compose, core config, and persisted plugin settings immediately.
- Traced the build mismatch to the release method: the previous active image contained a `CGO_ENABLED=1` binary even though an older saved cross-build artifact in its release directory was `CGO_ENABLED=0`.
- Rebuilt from the repository Dockerfile on the amd64 production host with `CGO_ENABLED=1`. An isolated candidate then proved the author plugin loaded, the plugin management route worked, and the core inject config was readable.
- Deployed corrected image `cli-proxy-api:codex-weekly-overdraft-6e8229af-cgo-amd64` first with the core feature absent/disabled; root=200, unauthenticated models=401, plugin loaded, restart=0, and OOM=false.
- Disabled the author plugin weekly-overdraft mutation in both host config and persisted plugin settings, then enabled the core at 10% canary with one pair and OAuth-only gating.
- Production `gpt-5.4` Responses smoke completed with HTTP 200. Final runtime counters reached `evaluated=169`, `injected=9`, `non-canary=80`, `unsupported-tail=80`, `success=7`, and `other-failure=1`; the container remained running with restart=0 and OOM=false, and current-container logs contained no warning/error lines.
- Updated the project release rule and Obsidian knowledge to require `CGO_ENABLED=1` plus isolated plugin-load validation for future production images.
