#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "${SCRIPT_DIR}/../.." && pwd)

COMPOSE_FILE="${REPO_ROOT}/deploy/cpa-router/compose.yaml"
ENV_FILE=""
PROJECT_NAME="cpa-router"
SERVICE_NAME="cpa-router"
CALLER_NETWORK="sub2api-access"
SHARED_ALIAS="cli-proxy-api"
EXPECTED_BACKEND=""
ROUTER_URL="http://127.0.0.1:18379"
PUBLIC_URL=""
CALLERS=()

usage() {
    cat <<'USAGE'
Usage:
  verify-router.sh [options]

Options:
  --compose-file PATH       Router Compose file.
  --env-file PATH           Explicit Compose environment file.
  --project-name NAME       Compose project name (default: cpa-router).
  --service NAME            Compose service name (default: cpa-router).
  --caller-network NAME     Shared caller network (default: sub2api-access).
  --shared-alias NAME       Sole router alias (default: cli-proxy-api).
  --expected-backend HOST:PORT
                            Require this backend in loaded Nginx config.
  --router-url URL          Router host probe base URL; empty disables it.
  --public-url URL          Optional public root probe URL.
  --caller CONTAINER        Repeat for every internal caller to check DNS.
  -h, --help                Show this help.

This command is read-only and never accepts or prints credentials.
USAGE
}

die() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

probe_url() {
    local label=$1 url=$2 expected_body=${3:-}
    local body
    body=$(curl --fail --silent --show-error --connect-timeout 3 --max-time 10 "$url") || die "$label probe failed: $url"
    if [[ -n "$expected_body" && "$body" != "$expected_body" ]]; then
        die "$label probe returned an unexpected body"
    fi
    printf '%s probe passed: %s\n' "$label" "$url"
}

while (($# > 0)); do
    case "$1" in
        --compose-file)
            (($# >= 2)) || die "--compose-file requires a value"
            COMPOSE_FILE=$2
            shift 2
            ;;
        --project-name)
            (($# >= 2)) || die "--project-name requires a value"
            PROJECT_NAME=$2
            shift 2
            ;;
        --env-file)
            (($# >= 2)) || die "--env-file requires a value"
            ENV_FILE=$2
            shift 2
            ;;
        --service)
            (($# >= 2)) || die "--service requires a value"
            SERVICE_NAME=$2
            shift 2
            ;;
        --caller-network)
            (($# >= 2)) || die "--caller-network requires a value"
            CALLER_NETWORK=$2
            shift 2
            ;;
        --shared-alias)
            (($# >= 2)) || die "--shared-alias requires a value"
            SHARED_ALIAS=$2
            shift 2
            ;;
        --expected-backend)
            (($# >= 2)) || die "--expected-backend requires a value"
            EXPECTED_BACKEND=$2
            shift 2
            ;;
        --router-url)
            (($# >= 2)) || die "--router-url requires a value"
            ROUTER_URL=$2
            shift 2
            ;;
        --public-url)
            (($# >= 2)) || die "--public-url requires a value"
            PUBLIC_URL=$2
            shift 2
            ;;
        --caller)
            (($# >= 2)) || die "--caller requires a value"
            CALLERS+=("$2")
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            die "unknown argument: $1"
            ;;
    esac
done

[[ -f "$COMPOSE_FILE" ]] || die "Compose file not found: $COMPOSE_FILE"
[[ -z "$ENV_FILE" || -f "$ENV_FILE" ]] || die "environment file not found: $ENV_FILE"
[[ "$PROJECT_NAME" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || die "invalid Compose project name"
[[ "$SERVICE_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid Compose service name"
[[ "$CALLER_NETWORK" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid caller network"
[[ "$SHARED_ALIAS" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid shared alias"
if [[ -n "$EXPECTED_BACKEND" ]]; then
    [[ "$EXPECTED_BACKEND" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*:[0-9]{1,5}$ ]] || die "expected backend must be HOST:PORT"
fi
for url in "$ROUTER_URL" "$PUBLIC_URL"; do
    [[ -z "$url" || "$url" =~ ^https?://[^[:space:]]+$ ]] || die "probe URLs must use http or https and contain no whitespace"
    [[ "$url" != *"@"* && "$url" != *"?"* && "$url" != *"#"* ]] || die "probe URLs must not contain userinfo, query strings, or fragments"
done
for caller in "${CALLERS[@]}"; do
    [[ "$caller" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid caller container: $caller"
done

for command_name in docker curl grep; do
    command -v "$command_name" >/dev/null 2>&1 || die "required command not found: $command_name"
done

compose=(docker compose)
if [[ -n "$ENV_FILE" ]]; then
    compose+=(--env-file "$ENV_FILE")
fi
compose+=(--project-name "$PROJECT_NAME" --file "$COMPOSE_FILE")
"${compose[@]}" config --quiet
container_id=$("${compose[@]}" ps -q "$SERVICE_NAME")
[[ -n "$container_id" ]] || die "router service container does not exist"
[[ "$(docker inspect --format '{{.State.Running}}' "$container_id")" == "true" ]] || die "router service is not running"
[[ "$(docker inspect --format '{{.RestartCount}}' "$container_id")" == "0" ]] || die "router service restart count is non-zero"
[[ "$(docker inspect --format '{{.State.OOMKilled}}' "$container_id")" == "false" ]] || die "router service was OOM-killed"

"${compose[@]}" exec -T "$SERVICE_NAME" nginx -t
rendered_config=$("${compose[@]}" exec -T "$SERVICE_NAME" nginx -T 2>&1)
if [[ -n "$EXPECTED_BACKEND" ]]; then
    grep -Fq "server ${EXPECTED_BACKEND};" <<<"$rendered_config" || die "loaded Nginx config does not contain expected backend $EXPECTED_BACKEND"
fi

network_container_ids=$(docker network inspect --format '{{range $id, $container := .Containers}}{{println $id}}{{end}}' "$CALLER_NETWORK") || die "caller network not found: $CALLER_NETWORK"
alias_owner_count=0
alias_owner_id=""
network_template="{{with index .NetworkSettings.Networks \"${CALLER_NETWORK}\"}}{{range .Aliases}}{{println .}}{{end}}{{end}}"
for network_container_id in $network_container_ids; do
    aliases=$(docker inspect --format "$network_template" "$network_container_id")
    if grep -Fxq "$SHARED_ALIAS" <<<"$aliases"; then
        ((alias_owner_count += 1))
        alias_owner_id=$network_container_id
    fi
done
((alias_owner_count == 1)) || die "expected one $SHARED_ALIAS alias owner, found $alias_owner_count"
[[ "$alias_owner_id" == "$container_id" ]] || die "the sole $SHARED_ALIAS alias owner is not the Compose-managed router"

router_network_ip=$(docker inspect --format "{{with index .NetworkSettings.Networks \"${CALLER_NETWORK}\"}}{{.IPAddress}}{{end}}" "$container_id")
[[ -n "$router_network_ip" ]] || die "router has no IPv4 address on $CALLER_NETWORK"
for caller in "${CALLERS[@]}"; do
    dns_output=$(docker exec "$caller" getent ahostsv4 "$SHARED_ALIAS" 2>/dev/null) || die "DNS lookup failed in caller $caller"
    awk -v expected="$router_network_ip" '
        $1 == expected { found = 1; next }
        NF > 0 { unexpected = 1 }
        END { exit !(found && !unexpected) }
    ' <<<"$dns_output" || \
        die "caller $caller did not resolve $SHARED_ALIAS to router $router_network_ip"
    printf 'Caller DNS passed: %s -> %s\n' "$caller" "$router_network_ip"
done

if [[ -n "$ROUTER_URL" ]]; then
    probe_url "router health" "${ROUTER_URL%/}/cpa-router-health" "cpa-router ok"
    probe_url "router direct root" "${ROUTER_URL%/}/"
fi
if [[ -n "$PUBLIC_URL" ]]; then
    probe_url "public root" "${PUBLIC_URL%/}/"
fi

printf 'Router verification passed: backend=%s alias-owner=%s restart=0 OOMKilled=false\n' \
    "${EXPECTED_BACKEND:-not-asserted}" "$SHARED_ALIAS"
