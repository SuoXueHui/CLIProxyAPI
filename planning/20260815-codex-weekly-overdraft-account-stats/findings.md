# Findings

- CPA core already passes the selected stable `Auth.ID` into the pure weekly-overdraft transform.
- Controller account rows expose the same identifier as `cpa_auth_id`; grouped rows retain each credential's `cpa_auth_id` in `credentials`.
- The existing Manager page already polls the core endpoint every 15 seconds and renders 5h/7d usage in a compact cell, so account-level status can reuse both paths without a new timer or table column.
- Existing global outcomes cross observe/inject hot changes. New per-auth outcomes must be action-scoped to preserve semantics.
- The account tracker uses `sync.Map` only for auth entry lookup/creation and per-entry atomics for steady-state requests; there is no global request mutex.
- The endpoint accepts repeated `auth-id` filters and returns a deterministic last-seen-descending account array; without filters it returns all active entries.
- Account entries are capped at 8192 and are removed after six hours of inactivity during status snapshots; hitting the cap drops account-only observability without changing the request or global metrics.
