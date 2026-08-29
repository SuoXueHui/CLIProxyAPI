# Findings

- The current scheduler is `sdk/cliproxy/auth/scheduler.go` and supports round-robin, fill-first, and weighted round-robin.
- Production can use a plugin scheduler, so adaptive filtering must apply before plugin picks as well as inside the built-in scheduler.
- `sdk/cliproxy/auth/conductor_stream.go` already buffers the first non-empty stream event in `readStreamBootstrap`, which is the stable first-event measurement point.
- Existing hard cooldowns mutate auth/model state and are persisted separately. The new adaptive penalty must remain in-memory and must not reuse `NextRetryAfter`.
- Existing stream completion and error handling is centralized in `wrapStreamResult`; an in-flight lease can be released there for successful, failed, and canceled streams.
- No native per-auth hard concurrency limit exists in the general scheduler path. The implementation therefore uses a non-blocking virtual load score and does not add a semaphore.
