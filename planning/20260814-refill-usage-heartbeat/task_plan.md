# Refill Usage Heartbeat Plan

## Goal

Emit a bounded control-only heartbeat on the authenticated CPA usage subscription so the independent refill controller can distinguish an idle source from a broken source.

## Stages

1. [completed] Confirm the production empty-pool deadlock and baseline tests.
2. [completed] Add a failing idle-subscription heartbeat test.
3. [completed] Add the 30-second usage-only heartbeat with queue-enabled gating.
4. [in_progress] Run full verification, merge master, deploy, and validate with the Controller.

## Boundaries

- English comments only in this repository.
- No request, auth, token, model, or cost data in the frame.
- One ticker per active usage subscription; stop it with the connection.
- Do not modify request routing or provider executors.
