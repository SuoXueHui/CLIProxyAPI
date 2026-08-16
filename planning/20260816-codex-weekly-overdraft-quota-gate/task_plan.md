# Task Plan

## Objective

Make Codex weekly-overdraft injection fail closed unless CPA runtime state has
already observed quota exhaustion for the selected OAuth credential.

## Scope

- Keep official ChatGPT quota reads out of the request path.
- Reuse CPA runtime auth/model quota state set by 429/usage-limit handling.
- Preserve observe mode, canary selection, pair strength, and six-hour metrics.
- Add focused regression coverage and verify the branch without production mutation.

## Steps

1. [completed] Record current data flow and choose the minimal runtime gate.
2. [completed] Add the request-local quota signal and inject guard.
3. [completed] Update tests, comments, config/docs where semantics are exposed.
4. [completed] Run focused, race, full, build, format, and diff verification.
5. [completed] Review release risk and reject production rollout of the gate.

## Decision

Do not merge or deploy this branch. A post-429 runtime gate conflicts with CPA
quota cooldown and auth selection: the exhausted credential is normally removed
from routing before another request can reach the transform. Production keeps
the existing 25% pre-injection canary because it avoids request-path quota calls,
on-429 replay, and scheduler changes.
