# xAI Plan Evidence Task Plan

## Goal

Expose non-sensitive, normalized xAI OAuth plan evidence from `GET /v0/management/auth-files` using only already-loaded auth metadata.

## Constraints

- Follow TDD: add tests first and record the expected red failure.
- Never return raw access, refresh, or ID tokens.
- Do not infer Free from a missing tier or a zero monthly limit.
- Preserve existing Codex response behavior.
- Keep the implementation local to management auth-file response construction.

## Phases

1. [completed] Trace auth-file entry construction and xAI metadata/token shapes.
2. [completed] Add focused failing tests for explicit metadata, access-token tier mapping, missing evidence, and secret safety.
3. [completed] Implement the minimum normalized plan extraction.
4. [completed] Run focused tests, formatting, broader relevant tests, and required build.
5. [completed] Review diff, update planning records, and commit.

## Errors

| Error | Attempt | Resolution |
|---|---:|---|
| Focused tests fail with missing `xai_plan_type` and `xai_plan_source` | 1 | Expected RED result; proceed with the minimal extraction implementation. |
| Secret-safety assertion matched the safe source name `access_token_tier` | 1 | Assert forbidden response keys and literal secret values instead of rejecting the documented source label. |
| Codex compatibility test asserted `map[string]any`, but the response uses `gin.H` | 1 | Assert the actual stable response type and values. |
| Endpoint test initially omitted `context.Context` for `Manager.Register` | 1 | Use `context.Background()` per the current method signature. |
| Full `go test ./...` failed in `internal/client/codex/live` | 1 | Unrelated WebRTC timing failure: `TestPionMediaRelayBridgesAudioAndDataChannel` timed out waiting for an upstream DataChannel. Re-run that package/test separately before final status. |
