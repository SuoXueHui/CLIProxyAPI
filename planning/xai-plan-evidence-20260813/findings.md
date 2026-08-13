# Findings

- `Handler.ListAuthFiles` uses `buildAuthFileEntry` for auth-manager-backed entries.
- Existing Codex JWT-derived fields are emitted by `extractCodexIDTokenClaims`; the xAI change should not modify that path.
- xAI OAuth metadata already contains `access_token`, `refresh_token`, `id_token`, expiration fields, and identity fields, but auth-file list responses intentionally expose only selected values.
- The implementation must treat auth metadata as evidence and emit normalized fields only; no network lookup is allowed.
- Existing project evidence shows current production access-token JWTs sometimes carry numeric `tier`, while freshly rotated tokens may omit it.
- The project already uses the `prod_auth.SubscriptionTier` mapping: `tier=0` is Free, `tier=1` is SuperGrok, and `tier=5` is SuperGrok Heavy. Other mapped tiers are outside this API's requested four-value contract and must remain unknown.
- Explicit metadata should take precedence over token decoding and should accept normalized aliases only when they resolve to `free`, `supergrok`, or `supergrok_heavy`.
- `last_refresh` is authentication refresh time, not necessarily when explicit plan metadata was observed, so the minimum response should omit `xai_plan_observed_at` unless a plan-specific timestamp is present.
