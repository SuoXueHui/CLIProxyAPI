# Codex Weekly Overdraft Core Implementation

## Goal
Implement the approved CPA-native Codex weekly overdraft request transform without relying on the account manager plugin.

## Approved Scope
- Default off with `observe` and `inject` modes.
- Stable canary bucketing and configurable pair count (`1`, `2`, or `4`).
- Shared, stateless HTTP/WebSocket transform.
- Eligible tails: user message, function call output, and custom tool call output.
- No synchronous probes, replay loop, scheduler changes, global request lock, or request-body retention.
- Preserve existing hard-stop and 429 cooldown behavior.

## Phases
- [completed] 1. Revalidate latest upstream architecture and establish a clean baseline.
- [completed] 2. Write the design and implementation plan for the approved scope.
- [completed] 3. Add config parsing, validation, and diff coverage with failing tests first.
- [completed] 4. Add the pure request transform and observability with failing tests first.
- [completed] 5. Integrate the shared transform into Codex HTTP and WebSocket paths with failing tests first.
- [completed] 6. Run focused, full, race, build, and formatting verification.
- [completed] 7. Review changes, update project knowledge where warranted, and summarize rollout guidance.

## Constraints
- Work only in branch `codex/codex-weekly-overdraft-core-20260815`.
- Do not modify the dirty primary checkout.
- Keep request payloads and credential identities out of logs.
- Skip paths should avoid copying the request body.
- Follow repository English-comment convention and run `gofmt` after Go changes.

## Errors
| Error | Attempt | Resolution |
|---|---:|---|
| `docs/superpowers` is ignored by the repository | 1 | Keep the approved design artifacts and add only their two exact paths with `git add -f`; do not change the global ignore policy. |
| zsh passed the newline-separated changed-file list to `gofmt` as one path | 1 | Switched to `git diff -z | xargs -0 gofmt -w`, preserving filenames and shell portability. |
| Full tests rejected YAML produced from a legacy zero-value `Config` | 1 | Added a failing compatibility test and normalized only the completely zero weekly-overdraft block to conservative disabled defaults. |
