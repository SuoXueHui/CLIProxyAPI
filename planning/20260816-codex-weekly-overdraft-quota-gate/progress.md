# Progress

## 2026-08-16

- Created isolated branch `codex/codex-weekly-overdraft-quota-gate` from
  `master` at `8c081fcb`.
- Confirmed the screenshot discrepancy is caused by quota-unaware canary
  injection, not by the official quota card calculating CORE counters.
- Chosen implementation: inject only when CPA runtime auth/model quota state
  reports exhaustion; no official quota request is added to the request path.
- Added a model-aware runtime quota query, request-local fail-closed gate,
  `quota-not-exhausted` skip counter, and focused helper/executor/management
  regressions.
- Focused auth, transform, executor, and management tests pass.
- Distinguished provider-confirmed Codex `usage_limit_reached` from transient
  `rate_limit_error/429` across HTTP, WebSocket, and CPA result state; focused
  and race tests pass, and the required server build passes.
- All packages except `internal/client/codex/live` pass uncached; that package
  also passes when its unrelated `TestPionMediaRelayBridgesAudioAndDataChannel`
  case is skipped. The same DataChannel timeout reproduces on clean `master`,
  proving it is not caused by this branch.
- Release review rejected the runtime-gate design because quota cooldown removes
  the exhausted auth from selection before the next request can inject. No
  source commit, merge, image build, config change, or deployment was made.
- Removed the rejected source/test/config/AGENTS diff from the isolated worktree
  after recording the evidence. The branch now retains planning evidence only,
  so no non-working candidate can be mistaken for a releasable change.
- Final read-only production check: CPA remains on
  `cli-proxy-api:codex-weekly-overdraft-accounts-d6753fd4-cgo-amd64` with
  `inject / 25% / 1 pair / OAuth-only`, restart=0, OOM=false; CPA root,
  Manager page, and Manager health all return 200, while Manager and Controller
  remain healthy.
