# xAI error visibility and disconnect recovery

## Goal
Preserve the complete xAI upstream error body for downstream management logs and prevent transient non-stream xAI disconnects from cascading into repeated `auth_unavailable` failures.

## Scope
- Trace the current non-stream xAI retry and auth result update paths on production `master`.
- Add focused regression tests before changing behavior.
- Keep streaming request behavior unchanged.
- Do not retry ordinary upstream HTTP 4xx/5xx responses.
- Verify focused packages, full tests, formatting, and server build.

## Phases
- [completed] 1. Reproduce and identify the uncovered 408/auth-unavailable path and error-body loss point.
- [completed] 2. Add failing regression tests for both behaviors.
- [completed] 3. Implement minimal fixes.
- [completed] 4. Run focused and full verification.
- [completed] 5. Review impact, sync durable project knowledge if warranted, and prepare release evidence.

## Errors
| Error | Attempts | Resolution |
|---|---:|---|
| Initial sparse-error wrapper also changed readable root `error` string payloads such as free-usage exhaustion | 1 | Added a regression test and narrowed wrapping to payloads without any readable message or root error string. |
