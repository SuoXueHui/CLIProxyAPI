# Codex Weekly Overdraft Core Merge and Production Release

## Goal
Merge the verified CPA-native Codex weekly-overdraft feature into maintained `master`, publish the updated fork branch, and deploy only `cli-proxy-api` to production with a reversible canary rollout.

## Phases
- [completed] 1. Verify feature branch, maintained master, and live production baseline.
- [completed] 2. Merge feature branch into master and resolve integration conflicts without dropping custom production fixes.
- [completed] 3. Run full tests, race tests, build, and diff review on merged master.
- [completed] 4. Push maintained master and prepare an amd64 production release with rollback artifacts.
- [completed] 5. Deploy the binary with the core feature initially disabled and verify service health.
- [completed] 6. Disable the plugin overdraft mutation, enable the core 10%/one-pair canary, and monitor health/metrics.
- [completed] 7. Record production evidence and update project knowledge.

## Safety Constraints
- Change only the maintained CLIProxyAPI master, its production image, and the two overdraft configuration blocks.
- Do not rebuild or restart Manager, new-api, Sub2API, databases, or other containers.
- Back up Compose, config, container inspect, current image ID, and relevant plugin settings before mutation.
- Keep the previous image locally and provide an executable rollback command.
- Never output management keys, API keys, OAuth tokens, or raw account credentials.

## Errors
| Error | Attempt | Resolution |
|---|---:|---|
| Merge conflict in `internal/api/server_test.go` | 1 | Both branches added independent management route tests at the same insertion point; retained both tests and confirmed both quota and overdraft routes pass. |
| Candidate host port `18317` was already allocated by CPA Manager Plus | 1 | Kept the candidate service isolated and remapped host port `28317` to candidate port `18317`. |
| Incremental image used a `CGO_ENABLED=0` cross-built binary, so the production `.so` plugin could not load | 1 | Immediately restored the previous image/config, proved the active production binary used `CGO_ENABLED=1`, rebuilt from the repository Dockerfile, and required an isolated plugin-load candidate before the second rollout. |
