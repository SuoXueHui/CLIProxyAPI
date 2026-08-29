# CPA Adaptive Auth Scheduling

## Purpose

Keep the existing ingress concurrency while avoiding new work on OAuth credentials that show slow first events or accumulate long-running streams. The mechanism is process-local and fail-open.

## Runtime state

The scheduler keeps bounded volatile state keyed by provider, model, and auth ID for health, plus provider and auth ID for aggregate in-flight load. Auth files, quota state, and persisted cooldown records are not modified.

## Selection

After hard availability and request eligibility checks, penalized credentials are excluded only when at least one non-penalized candidate exists. If all candidates are penalized, the scheduler selects from the full ready set. Within the highest ready priority, the virtual load score is:

```text
in_flight * max(duration_ewma, load_floor) / max(static_weight, 1)
```

The lowest score wins; existing round-robin or weighted tie-breaking remains in effect. Explicit session pins remain authoritative unless hard availability requires failover.

## Observations

The first non-empty stream event is measured at the existing bootstrap boundary. The lease remains active until the wrapped stream ends, fails, or is canceled. Non-stream executions release the lease after the upstream call returns. Failures continue through the existing hard cooldown path independently.

## Soft penalty

After the minimum sample count, consecutive first-event samples at or above the configured threshold create an in-memory penalty. The default sequence is 30 seconds, 60 seconds, 2 minutes, 4 minutes, capped at 5 minutes. A healthy sample clears the slow streak. Soft penalties never return an unavailable error and are cleared on process restart or when adaptive scheduling is disabled.

## Plugin compatibility

Legacy/plugin candidate lists are filtered before plugin selection. Candidate metadata exposes the current adaptive load and penalty deadline when enabled. Plugin delegation to a built-in strategy still uses the adaptive built-in scheduler.

## Configuration

The feature is opt-in under `routing.adaptive-auth`. Duration fields accept YAML duration strings. Invalid values are rejected during config parsing and hot reload.

## Safety invariants

- No global semaphore or new hard per-auth limit is introduced.
- Existing streams are never migrated or terminated by adaptive scheduling.
- Soft state cannot cause a 503; all-soft candidates fail open.
- Existing hard cooldown and quota semantics remain unchanged.
- Runtime state is bounded by TTL and maximum entry count.
