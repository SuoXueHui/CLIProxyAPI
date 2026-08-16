# Findings

- CPA core already passes the selected stable `Auth.ID` into the pure weekly-overdraft transform.
- Controller account rows expose the same identifier as `cpa_auth_id`; grouped rows retain each credential's `cpa_auth_id` in `credentials`.
- The existing Manager page already polls the core endpoint every 15 seconds and renders 5h/7d usage in a compact cell, so account-level status can reuse both paths without a new timer or table column.
- Existing global outcomes cross observe/inject hot changes. New per-auth outcomes must be action-scoped to preserve semantics.
- The account tracker uses `sync.Map` only for auth entry lookup/creation and per-entry atomics for steady-state requests; there is no global request mutex.
- The endpoint accepts repeated `auth-id` filters and returns a deterministic last-seen-descending account array; without filters it returns all active entries.
- Account entries are capped at 8192 and are removed after six hours of inactivity during status snapshots; hitting the cap drops account-only observability without changing the request or global metrics.
- The live CPA and Manager images still point to the earlier global-panel release commits (`6e8229af` and `ef4bbd92` respectively). Publishing the account extension therefore requires replacing only those two images; Controller and its data model remain unchanged.
- Final production evidence aligns global and account counters: `evaluated=22`, `injected=2`, global success=2, two account entries with injected requests, and summed per-account injected success=2. Two visible rows also have 7d official quota at 100%, but the UI correctly labels the CORE data as operational evidence rather than guaranteed extra entitlement.
- A fresh cross-day check after about 14 hours of runtime found 74 active six-hour account entries and 923 account-level injected requests: 540 success, 81 usage-limit, 298 hard-stop, 0 canceled, and 4 other-failure. CPA and Manager both remained restart=0/OOM=false, and the browser console remained clean.
- The author plugin remains enabled and registered at v0.3.1332 for account management, while its `weekly_overdraft_enabled` experiment switch remains false. The active 10%/1-pair injection therefore still comes only from the CPA core implementation.
