# Findings

- The current transform gates on enabled config, OAuth classification, request
  shape, idempotency, and deterministic canary only; it does not inspect quota.
- The Manager quota endpoint fetches official 5h/7d data on demand and is not a
  request-path cache. Calling it for every request would add upstream traffic,
  latency, and another failure surface.
- CPA records `Quota.Exceeded` and model-level `Quota.Exceeded` after quota
  failures. These values are copied into the selected auth snapshot passed to
  the executor.
- The auth selector normally cools quota-exceeded credentials, so this first
  change intentionally does not alter scheduler/cooldown behavior or add an
  on-429 replay. It is a fail-closed correctness gate, not a guarantee that a
  cooled credential will be selected for an overdraft attempt.
- Observe mode remains useful for preflight candidate measurement; only actual
  mutation is blocked when runtime quota exhaustion is absent.
- The runtime helper resolves model-specific quota state before the aggregate
  auth value. A quota error on another model therefore cannot authorize
  mutation for the current model.
- The management status DTO now includes a `quota-not-exhausted` skip counter;
  fresh accounts do not create per-account CORE strips because no overdraft
  action occurred.
- The generic CPA selector blocks a credential after confirmed quota exhaustion
  until its recovery window. Consequently a gate based only on that runtime
  state is too late for the next normal request and would make overdraft
  injection effectively inert under the default cooldown policy.
- The PoC and current production implementation are intentionally proactive:
  injection means the request shape was prepared for the experiment, not that
  official quota was exhausted or extra entitlement was consumed.
- An actual overdraft success requires a fresh official quota snapshot at 100%
  plus a successful injected response. Injection and success counters alone do
  not prove overdraft while official quota remains available.
- The safe alternatives are either bounded asynchronous official-quota caching
  or a strictly one-shot on-usage-limit replay. Both expand concurrency/retry
  behavior and are deferred rather than mixed into this stability fix.
