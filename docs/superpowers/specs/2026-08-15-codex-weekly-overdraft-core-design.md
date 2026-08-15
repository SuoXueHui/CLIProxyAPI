# CPA-Native Codex Weekly Overdraft Design

## Goal

Add an experimental, switchable Codex 5-hour/7-day quota-overdraft request transform to CLIProxyAPI core without depending on a plugin, background probe, or scheduler change.

## Scope

The first release performs one stateless transform immediately before Codex requests are sent upstream. It supports HTTP, SSE, and WebSocket transports through one shared helper. The feature is disabled by default and can run in `observe` mode before payload mutation is enabled.

The first release does not add on-429 replay, five-attempt probes, token-drain routing, account enable/disable behavior, persistence, or custom retry policy. Existing 401/402/403 handling, 429 cooldowns, credential rotation, and session affinity remain authoritative.

## Configuration

```yaml
codex:
  weekly-overdraft:
    enabled: false
    mode: observe
    canary-percent: 10
    pair-count: 1
    tail-policy: user-and-tool-output
    oauth-only: true
    max-body-bytes: 8388608
```

- `enabled`: global kill switch. Default `false`.
- `mode`: `observe` records eligibility and outcomes without changing the request; `inject` appends tool pairs. Default `observe`.
- `canary-percent`: stable percentage bucket derived from selected auth and session identity. Valid range `1..100`; default `10`.
- `pair-count`: allowed values `1`, `2`, or `4`; default and recommended value `1`.
- `tail-policy`: `user-only` or `user-and-tool-output`; default `user-and-tool-output`.
- `oauth-only`: limits the experiment to Codex OAuth credentials. Default `true`.
- `max-body-bytes`: payload safety limit. Valid range `1..33554432`; default `8388608`.

Invalid values fail config loading instead of being silently broadened.

## Request Transform

The transform receives the effective config, selected auth identity, OAuth classification, stable session identity, and final Codex request body. It returns the original slice on every skip path. It allocates a replacement body only when `mode=inject` and all guards pass.

Eligibility requires:

1. The feature is enabled.
2. The selected credential is OAuth when `oauth-only=true`.
3. The body is valid JSON, within the configured size limit, and contains a non-empty `input` array.
4. No existing CPA/plugin overdraft marker is present.
5. The final item is one of:
   - `message` with `role=user`;
   - `function_call_output` when the tail policy permits tool outputs;
   - `custom_tool_call_output` when the tail policy permits tool outputs.
6. The stable auth/session bucket is within `canary-percent`.

Injection appends `pair-count` linked `custom_tool_call` and `custom_tool_call_output` pairs using the measured no-op `exec` wire shape. Call IDs are deterministic hashes of auth, session, current input, and pair index. Only hashes are retained in the request; no prompt or credential data is logged or persisted.

The helper recognizes both the core prefix and the historical plugin prefix so HTTP retries, WebSocket resend, and mixed deployments cannot inject twice.

## Integration

HTTP non-streaming and streaming requests call the helper after thinking, payload rules, tool normalization, multi-agent optimization, and reasoning replay, but before cache/identity rewriting and request construction.

WebSocket non-streaming and streaming requests call the same helper at the corresponding point before prompt-cache headers, identity rewriting, and `response.create` construction. A WebSocket send retry reuses the already transformed payload and never applies the transform again.

`responses/compact` and image-generation request paths are excluded.

## Observability

Lock-free atomic counters record:

- total evaluations;
- skip reasons (`disabled`, `non-oauth`, `oversize`, `malformed`, `unsupported-tail`, `already-injected`, `non-canary`);
- `observed` and `injected` requests;
- terminal outcomes (`success`, `usage-limit`, `hard-stop`, `canceled`, `other-failure`).

`GET /v0/management/codex-weekly-overdraft` returns the effective config and the process-local counter snapshot. It never returns auth IDs, session IDs, prompts, request bodies, tokens, or account labels. Existing `PUT /v0/management/config.yaml` remains the write path and hot-reload mechanism.

## Concurrency and Memory Safety

- No global request mutex, semaphore, singleflight, or synchronous probe is introduced.
- Counters use atomics; request decisions are immutable local values.
- Skip paths return the original byte slice and do not clone the payload.
- Injection creates one final body and does not retain the original body beyond the existing request lifecycle.
- Existing retry and WebSocket resend paths reuse the transformed payload.

## Verification

Unit tests cover defaults, validation, config diffs, OAuth gating, the three eligible tails, unsupported tails, stable canary selection, `1/2/4` pairs, idempotency, malformed and oversized bodies, and zero-copy skip behavior.

Executor tests prove both HTTP and WebSocket upstream payloads contain exactly one transform application. Management tests verify the status endpoint redacts request and credential data. Focused race tests and benchmarks verify the helper has no shared request lock and that disabled/skip paths allocate no replacement payload.

Rollout order is `disabled` -> `observe` -> `inject` at 10% with one pair -> 25% -> 50% -> 100%. Any regression in 429 rate, TTFT, RSS, goroutines, connection count, or successful post-threshold tokens triggers rollback by setting `enabled: false`.
