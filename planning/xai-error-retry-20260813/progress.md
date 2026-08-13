# Progress

## 2026-08-13
- Checked repository `AGENTS.md`; it exists and defines the production master worktree and verification requirements.
- Confirmed the primary working directory contains unrelated modifications and created an isolated feature worktree from clean production `master=97c3f8a0`.
- Initialized an isolated planning directory for this task.
- Added RED regression tests for explicit lifecycle classification, auth fallback without cooldown, and sparse xAI error-body compatibility.
- Implemented a typed xAI incomplete-stream error that preserves HTTP 408 while marking the failure as connection lifecycle noise.
- Added a generic executor-to-auth lifecycle interface so status-bearing transport failures can skip credential cooldown without becoming request-scoped.
- Added sparse xAI error wrapping that preserves the complete original JSON while supplying OpenAI-compatible message/type/code fields; standard readable error bodies remain unchanged.
- Focused tests passed. The first broader executor run exposed a free-usage root `error` string compatibility case; narrowed the wrapper and added a regression test before rerunning.
- Read-only production verification confirmed the deployed build already contains the previous internal retry and that the live configuration permits two credential attempts.
- Correlated representative 408 request IDs in live logs: some requests rotated across two xAI credentials before the final 408, while later requests had only one selectable credential. This confirms terminal 408 cooldown is the source of the auth-pool cascade.
- No production configuration, credential, container, database, or plugin state was changed during diagnosis.
- During self-review, added a RED test for the final body-read truncation path; it initially failed because the executor returned raw `unexpected EOF`. Updated that path to return the typed 408 lifecycle error after retries are exhausted, then verified the focused retry cases pass.
- Fresh verification passed after the final change: xAI error visibility tests, affected executor/auth/executor-type packages, `git diff --check`, required server build, and full `go test ./... -count=1`.
- Reviewed project `AGENTS.md`; no new durable code rule was needed.
- Checked project knowledge sync rules and updated `07-问题排查.md` plus `08-变更记录.md` with the 408→503 cascade, lifecycle classification boundary, sparse error-body compatibility, tests, and current unpublished state. No secret values were recorded.

- Independent review identified the sparse-error request-fault misclassification and WebSocket message propagation gap. Added RED regressions, corrected both paths, and reran focused tests.
- Final fresh verification after review fixes passed: `git diff --check`, affected package tests, required server build, and full `go test ./... -count=1`.
