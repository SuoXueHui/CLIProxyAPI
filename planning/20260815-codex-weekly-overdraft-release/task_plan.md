# Codex Weekly Overdraft Core Merge and Production Release

## Goal
Merge the verified CPA-native Codex weekly-overdraft feature into maintained `master`, publish the updated fork branch, and deploy only `cli-proxy-api` to production with a reversible canary rollout.

## Phases
- [completed] 1. Verify feature branch, maintained master, and live production baseline.
- [in_progress] 2. Merge feature branch into master and resolve integration conflicts without dropping custom production fixes.
- [pending] 3. Run full tests, race tests, build, and diff review on merged master.
- [pending] 4. Push maintained master and prepare an amd64 production release with rollback artifacts.
- [pending] 5. Deploy the binary with the core feature initially disabled and verify service health.
- [pending] 6. Disable the plugin overdraft mutation, enable the core 10%/one-pair canary, and monitor health/metrics.
- [pending] 7. Record production evidence and update project knowledge.

## Safety Constraints
- Change only the maintained CLIProxyAPI master, its production image, and the two overdraft configuration blocks.
- Do not rebuild or restart Manager, new-api, Sub2API, databases, or other containers.
- Back up Compose, config, container inspect, current image ID, and relevant plugin settings before mutation.
- Keep the previous image locally and provide an executable rollback command.
- Never output management keys, API keys, OAuth tokens, or raw account credentials.

## Errors
| Error | Attempt | Resolution |
|---|---:|---|
