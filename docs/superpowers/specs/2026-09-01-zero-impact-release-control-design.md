# Zero-Impact CPA Release Control Design

## Objective

Add the minimum lifecycle and routing controls required to release CPA without recreating the active listener or allowing two containers to concurrently own OAuth writes, account IPv6 addresses, plugin auth mutations, or the same usage outbox.

## Runtime Modes

- `active`: accepts proxy traffic, holds the credential writer lease, allows credential persistence and request-triggered refresh, runs auto-refresh, enables account IPv6, and allows plugin auth mutation.
- `standby`: exposes health and lifecycle management only. Proxy traffic is rejected with HTTP 503 and `Retry-After`; no writer lease, refresh, account IPv6, or auth mutation is allowed.
- `serving-readonly`: accepts proxy traffic using the loaded credential snapshot over IPv4. Request-triggered refresh and request-auth preparation are skipped, persistence and plugin auth mutation are disabled, and usage is written to the instance's independent outbox.
- `draining`: rejects new proxy requests while requests already admitted continue. Background auto-refresh is stopped, but the active writer and IPv6 ownership are retained until the admitted request count reaches zero.

The default is `active` for backward compatibility. Instance mode is selected with `CLIPROXY_LIFECYCLE_MODE`; it is not stored in shared `config.yaml`.

## Writer Lease

Active instances acquire an exclusive process lock before enabling credential writes. The path is selected by `CLIPROXY_WRITER_LOCK_PATH`; deployment uses a stable host-mounted runtime directory. The lock is advisory for legacy binaries, so the first migration must stop the legacy writer before promoting the new instance. Subsequent releases are fenced automatically.

The writer gate applies to:

- request-triggered credential refresh;
- request-auth preparation that mutates metadata;
- auth persistence and cooldown persistence;
- file-store Save/Delete and load-time repairs;
- plugin `host.auth.save` callbacks;
- mutating management endpoints while the instance is not active.

Runtime-only selection, cooldown, and result state may still change in memory in `serving-readonly`.

## HTTP Admission

A server middleware classifies lifecycle and health/control routes separately from proxy routes.

- `standby` and `draining` reject new proxy routes.
- `serving-readonly` and `active` admit proxy routes and increment an active-request counter for the full handler lifetime.
- GET/HEAD management reads remain available in every mode.
- Mutating management routes are allowed only in `active`, except the authenticated lifecycle transition endpoint.

The management API provides:

- `GET /v0/management/lifecycle`
- `PUT /v0/management/lifecycle` with `target` and optional `expected_generation`

Transitions are serialized, generation-checked, and idempotent.

## Transitions

### Standby To Serving Readonly

Enable proxy admission only. Keep writes, refresh, plugin mutations, and IPv6 disabled.

### Serving Readonly To Active

1. Acquire the writer lease.
2. Reload the current auth snapshot from stable storage.
3. Enable credential and plugin auth writes.
4. Enable account IPv6 and verify assignment succeeds.
5. Start auto-refresh.
6. Publish `active` only after all prerequisites succeed.

Failure rolls back the partial activation and remains `serving-readonly`.

### Active To Draining

Publish `draining`, reject new proxy requests, and stop background auto-refresh. Existing admitted requests retain the writer and IPv6 ownership.

### Draining To Standby

Wait for admitted requests to reach zero, disable auth/plugin writes, release all account IPv6 addresses, release the writer lease, and publish `standby`.

## Usage Outbox

Every blue/green instance uses a distinct data directory and outbox. `serving-readonly` records usage normally so no request accounting is discarded. The release procedure drains the old outbox before stopping blue, then switches the Manager collector to green and drains the green backlog. No two containers open the same SQLite outbox.

## Internal Router

A Compose-managed Nginx `cpa-router` becomes the sole `cli-proxy-api` alias owner on `sub2api-access`. CPA instances connect only to a dedicated `cpa-router-backend` network with unique names `cpa-blue` and `cpa-green`.

The router disables response/request buffering, preserves HTTP/1.1 Upgrade headers, uses 24-hour read/write timeouts, and disables automatic upstream retry. Backend changes use `nginx -t` followed by HUP reload so existing HTTP/SSE/WebSocket connections remain on the old worker while new connections use the new backend.

The first Docker alias handoff uses temporary NEW-connection DNAT in caller network namespaces so cached old-IP resolutions reach the router while existing established connections remain intact. Rules are removed after the old direct path reaches zero and caller DNS consistently resolves the router.

## First Release Sequence

1. Build and smoke-test the new image without live auth or shared writable state.
2. Install `cpa-router`, initially forwarding to the current legacy CPA.
3. Migrate public Nginx and internal callers to the router while it still targets legacy blue.
4. Start green in `standby`, sharing stable auth read access but using independent plugin/data/log/outbox paths and no account IPv6.
5. Move green to `serving-readonly`; reload router to green.
6. Drain all legacy blue connections and its outbox, then stop legacy blue.
7. Promote green to `active`, verify IPv6 from its namespace, switch Manager collection, and drain the green backlog.
8. Preserve blue image and release evidence for rollback.

## Rollback

- Before legacy blue stops: reload router to blue and return green to standby.
- After legacy blue stops but before green activates: keep green serving read-only while restarting and verifying blue, then reload router to blue.
- After green activates: drain green, demote it to standby, release IPv6/writer ownership, start blue with the latest stable auth snapshot, and reload router.

## Acceptance

- Exactly one credential writer lease holder and one account IPv6 owner.
- Exactly one `cli-proxy-api` alias owner.
- Public, direct, NewAPI, and Sub2API probes succeed.
- Plugin version/path/checksum and registration are correct.
- Auth file count and stable config checksum remain consistent.
- IPv6 namespace bind/TCP/HTTP probe succeeds with zero tentative/dadfailed addresses.
- Outbox accounting has no unexplained delta, ack failure, or dead letter growth.
- No new release-attributable HTTP 000/502/503, panic, OOM, restart, bind failure, or connection timeout.

## Scope Boundary

This design does not introduce a separate credential service or migrate existing TCP connections between processes. `serving-readonly` is intentionally short and may fail a request that requires token refresh; the release gate requires access-token headroom and immediate rollback on new authentication failures.
