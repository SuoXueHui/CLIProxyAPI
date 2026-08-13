# Findings: Request Monitoring Release (2026-08-13)

- Prior live reconciliation proved the affected account finalized 625 requests and 542 HTTP 200 responses, while Manager SQLite retained none of those successes and only four later zero-token failures.
- The Manager cost formula was independently reproduced exactly; the `$0.00` side was incomplete collection, not bad arithmetic.
- CLIProxyAPI durable outbox and Manager claim/ack changes are already committed and pushed to the maintained forks.
- CLIProxyAPI local/fork master currently resolves to `2f034843`; CPA Manager fork master resolves to `c153d2d7`, while local Manager master has one newer UI clarification commit `f69d13d9` that must be reviewed/pushed before release.
- The primary CLIProxyAPI workspace is dirty with unrelated changes, so release work uses a separate worktree.

## Live deployment verification

- CLIProxyAPI production image: `cli-proxy-api:master-v7.2.130-monitoring-2f034843`.
- Manager production image after the concurrent master rollout: `cpa-manager-plus:usage-window-f69d13d9-amd64`.
- Both images include the request-monitoring durable delivery chain: CLI outbox commits `8cc85564`, `243a704f`, `5db6271e`; Manager claim/commit/ack commits `3c348335`, `272175dd`, `5c5f5683` via merge `9de94826`.
- Live CLI config persists the outbox at `/CLIProxyAPI/data/usage-outbox.sqlite` on the existing `/data/apps/cli-proxy-api/data` host mount.
- At the verification snapshot, proxy counters reconciled exactly (`produced=19336`, `acked=19336`, no pending/inflight). Manager collector was running in HTTP claim/ack mode with no ack failures or dead letters.
- Rollback snapshots exist under the production release directories for both components; old images remain available.
