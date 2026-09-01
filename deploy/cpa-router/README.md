# CPA Router Release Runbook

This directory is the reviewed template for the Compose-managed internal CPA
router. It is intentionally a separate release unit from both CPA instances:

- Compose project and service: `cpa-router`
- loopback-only host port: `18379` (CPA continues to listen on container port `8317`)
- caller network: `sub2api-access`
- backend-only network: `cpa-router-backend`
- backend names: `cpa-blue` and `cpa-green`

The router is the only final owner of the `cli-proxy-api` caller-network alias.
CPA containers must not publish that alias after the first handoff. Each CPA
release must retain independent plugin, data, log, and usage-outbox paths.

## Safety Properties

- Commands that alter a backend or caller namespace are dry-run by default.
- `reload-backend.sh` validates a candidate in a one-shot Compose container,
  takes a same-directory reload lock, atomically replaces `backend.conf`, runs
  `nginx -t`, and only then sends HUP. A failed test or reload restores the prior
  file and attempts to reload it.
- Nginx disables request/response buffering and upstream retries, preserves
  HTTP/1.1 upgrades, and allows 24-hour SSE/WebSocket reads and writes.
- Caller DNAT matches only TCP conntrack state `NEW`. Existing connections keep
  their original path. Rule removal fails closed until caller DNS is stable and
  direct legacy conntrack entries reach zero.
- Scripts accept no credential values. Run authenticated lifecycle, model, and
  usage checks separately without shell tracing or printing authorization data.

## 1. Prepare Stable Files And Baseline

Never use historical names, IPs, counts, or versions as the live baseline.
Record the active container, network endpoint, mounts, image digest, restart/OOM
state, auth count, plugin checksum, and current-window errors first.

Install this directory at a stable host path, for example:

```bash
ROUTER_DIR=/data/apps/cli-proxy-api/router
install -d -m 0755 "$ROUTER_DIR/nginx/conf.d"
install -m 0644 deploy/cpa-router/compose.yaml "$ROUTER_DIR/compose.yaml"
install -m 0644 deploy/cpa-router/nginx/nginx.conf "$ROUTER_DIR/nginx/nginx.conf"
install -m 0644 deploy/cpa-router/nginx/conf.d/backend.conf "$ROUTER_DIR/nginx/conf.d/backend.conf"
install -m 0644 deploy/cpa-router/nginx/conf.d/proxy.conf "$ROUTER_DIR/nginx/conf.d/proxy.conf"
install -m 0644 deploy/cpa-router/nginx/conf.d/proxy-params.inc "$ROUTER_DIR/nginx/conf.d/proxy-params.inc"
```

Copy `.env.example` to `$ROUTER_DIR/.env`, pin `CPA_ROUTER_IMAGE` to the
reviewed digest, and verify the distinct project, network, alias, and port values.
The whole `conf.d` directory is mounted so atomic file replacement remains
visible to Nginx; do not change it to a single-file `backend.conf` bind mount.

```bash
docker compose --env-file "$ROUTER_DIR/.env" --project-name cpa-router --file "$ROUTER_DIR/compose.yaml" config
docker network inspect sub2api-access
docker network inspect cpa-router-backend
```

Create `cpa-router-backend` only when the read-only inspect confirms it is
absent. This is an explicit one-time mutation:

```bash
docker network create cpa-router-backend
```

## 2. Attach Blue And Start The Router

Set these values from live inspection. Do not reuse the placeholders:

```bash
LEGACY_CONTAINER=<current-cpa-container>
CALLER_A=<newapi-container>
CALLER_B=<sub2api-container>
```

Attach legacy blue to the backend network under a unique name without changing
its existing caller endpoint:

```bash
docker network connect --alias cpa-blue cpa-router-backend "$LEGACY_CONTAINER"
```

Keep `backend.conf` on `cpa-blue:8317`, start only the router project, and test
both Nginx syntax and the legacy proxy path before any caller handoff:

```bash
docker compose --env-file "$ROUTER_DIR/.env" --project-name cpa-router --file "$ROUTER_DIR/compose.yaml" up -d cpa-router
docker compose --env-file "$ROUTER_DIR/.env" --project-name cpa-router --file "$ROUTER_DIR/compose.yaml" exec -T cpa-router nginx -t
curl --fail --silent --show-error http://127.0.0.1:18379/cpa-router-health
curl --fail --silent --show-error http://127.0.0.1:18379/
```

At this point Docker DNS may temporarily return both legacy and router for
`cli-proxy-api`. This is expected only during the first handoff. Both paths must
still target blue before proceeding.

## 3. Install Temporary Caller DNAT

Read both IPs from the live shared network:

```bash
OLD_IP=$(docker inspect --format '{{with index .NetworkSettings.Networks "sub2api-access"}}{{.IPAddress}}{{end}}' "$LEGACY_CONTAINER")
ROUTER_ID=$(docker compose --env-file "$ROUTER_DIR/.env" --project-name cpa-router --file "$ROUTER_DIR/compose.yaml" ps -q cpa-router)
ROUTER_IP=$(docker inspect --format '{{with index .NetworkSettings.Networks "sub2api-access"}}{{.IPAddress}}{{end}}' "$ROUTER_ID")
printf 'legacy=%s router=%s\n' "$OLD_IP" "$ROUTER_IP"
```

Run the default dry-run for every caller, review the exact endpoints, then add
the rule explicitly. Root is required because the host enters each caller's
network namespace. The host must provide `nsenter`, `iptables`, `conntrack`, and
`curl`; each caller must provide `getent` for Docker DNS verification.

```bash
sudo scripts/cpa-router/caller-dnat.sh install --caller "$CALLER_A" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP"
sudo scripts/cpa-router/caller-dnat.sh install --caller "$CALLER_A" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP" --apply

sudo scripts/cpa-router/caller-dnat.sh install --caller "$CALLER_B" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP"
sudo scripts/cpa-router/caller-dnat.sh install --caller "$CALLER_B" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP" --apply
```

The apply path probes `/cpa-router-health` through the old destination. A failed
probe removes the just-added rule. Do not stop or disconnect legacy blue yet.

## 4. Switch Backends Without Recreating The Router

Start green as a distinct Compose project/service on `cpa-router-backend`, with
the unique alias `cpa-green`, an unused host port if one is needed, and stable
per-instance writable mounts. It must reach `serving-readonly` before routing
traffic to it.

Dry-run, apply, and verify the backend reload:

```bash
scripts/cpa-router/reload-backend.sh \
  --compose-file "$ROUTER_DIR/compose.yaml" \
  --env-file "$ROUTER_DIR/.env" \
  --backend-file "$ROUTER_DIR/nginx/conf.d/backend.conf" \
  --backend cpa-green:8317 \
  --management-backend cpa-blue:8317

scripts/cpa-router/reload-backend.sh \
  --compose-file "$ROUTER_DIR/compose.yaml" \
  --env-file "$ROUTER_DIR/.env" \
  --backend-file "$ROUTER_DIR/nginx/conf.d/backend.conf" \
  --backend cpa-green:8317 \
  --management-backend cpa-blue:8317 \
  --apply
```

HUP leaves existing HTTP/SSE/WebSocket connections on the old Nginx workers;
new proxy connections use green. Management and usage collection stay on blue
until its existing requests and durable outbox are both drained. No `docker
compose up`, restart, or recreate belongs in a routine backend switch.

## 5. Finish The First Alias Handoff

Run handoff verification for each caller. `old-established` counts only direct
legacy conntrack entries; DNAT entries have the router as their reply source and
do not block the direct-path drain. The command fails closed until the direct
count is zero; wait for existing connections to finish and rerun it.

```bash
sudo scripts/cpa-router/caller-dnat.sh verify --caller "$CALLER_A" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP" --allow-additional-dns
sudo scripts/cpa-router/caller-dnat.sh verify --caller "$CALLER_B" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP" --allow-additional-dns
```

Only after both direct counts reach zero, the legacy request drain is zero, and
its usage outbox is drained may legacy blue be stopped. Promote green to active,
then reload both proxy and management backends to green. Only after those gates
may blue be disconnected from `sub2api-access`; that operation removes the
duplicate alias owner.

```bash
scripts/cpa-router/reload-backend.sh \
  --compose-file "$ROUTER_DIR/compose.yaml" \
  --env-file "$ROUTER_DIR/.env" \
  --backend-file "$ROUTER_DIR/nginx/conf.d/backend.conf" \
  --backend cpa-green:8317 \
  --management-backend cpa-green:8317 \
  --apply
```

Confirm the router is now the sole alias owner and every caller resolves its IP:

```bash
scripts/cpa-router/verify-router.sh \
  --compose-file "$ROUTER_DIR/compose.yaml" \
  --env-file "$ROUTER_DIR/.env" \
  --expected-backend cpa-green:8317 \
  --expected-management-backend cpa-green:8317 \
  --caller "$CALLER_A" \
  --caller "$CALLER_B"
```

Remove each temporary DNAT rule only after three successful DNS checks and zero
direct legacy entries. Removal is also dry-run first:

```bash
sudo scripts/cpa-router/caller-dnat.sh remove --caller "$CALLER_A" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP"
sudo scripts/cpa-router/caller-dnat.sh remove --caller "$CALLER_A" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP" --apply

sudo scripts/cpa-router/caller-dnat.sh remove --caller "$CALLER_B" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP"
sudo scripts/cpa-router/caller-dnat.sh remove --caller "$CALLER_B" --old-ip "$OLD_IP" --router-ip "$ROUTER_IP" --apply
```

Repeat `verify-router.sh`, then perform authenticated direct/public/model probes,
IPv6 namespace TCP/HTTP validation, plugin/version/checksum checks, auth-count
comparison, and a short usage/event/error observation window.

## Rollback

Before legacy blue stops, route new connections back to it with the same
validated reload mechanism:

```bash
scripts/cpa-router/reload-backend.sh \
  --compose-file "$ROUTER_DIR/compose.yaml" \
  --env-file "$ROUTER_DIR/.env" \
  --backend-file "$ROUTER_DIR/nginx/conf.d/backend.conf" \
  --backend cpa-blue:8317 \
  --management-backend cpa-blue:8317

scripts/cpa-router/reload-backend.sh \
  --compose-file "$ROUTER_DIR/compose.yaml" \
  --env-file "$ROUTER_DIR/.env" \
  --backend-file "$ROUTER_DIR/nginx/conf.d/backend.conf" \
  --backend cpa-blue:8317 \
  --management-backend cpa-blue:8317 \
  --apply
```

Keep caller DNAT installed while the router targets blue; this preserves cached
old-IP and router-IP callers. Return green to standby through the authenticated
lifecycle API without logging the authorization value.

If the legacy alias has already been removed, restart and verify blue on
`cpa-router-backend` first, reload the router to blue, and only then restore any
caller-network alias. To remove a DNAT rule after rolling DNS back to legacy,
require repeated legacy-IP resolution explicitly:

```bash
sudo scripts/cpa-router/caller-dnat.sh remove \
  --caller "$CALLER_A" \
  --old-ip "$OLD_IP" \
  --router-ip "$ROUTER_IP" \
  --expected-dns-ip "$OLD_IP" \
  --apply
```

After green has become active, first drain it to standby so it releases the
writer lease and account IPv6 ownership. Start and validate blue from the latest
stable auth snapshot, promote blue, then reload the router. Never run blue and
green as simultaneous credential writers or IPv6 owners.
