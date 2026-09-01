#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "${SCRIPT_DIR}/../.." && pwd)

COMPOSE_FILE="${REPO_ROOT}/deploy/cpa-router/compose.yaml"
BACKEND_FILE="${REPO_ROOT}/deploy/cpa-router/nginx/conf.d/backend.conf"
ENV_FILE=""
PROJECT_NAME="cpa-router"
SERVICE_NAME="cpa-router"
BACKEND=""
MANAGEMENT_BACKEND=""
KEEPALIVE=64
APPLY=0

usage() {
    cat <<'USAGE'
Usage:
  reload-backend.sh --backend HOST:PORT --management-backend HOST:PORT [options]

Options:
  --backend HOST:PORT     Required backend, for example cpa-green:8317.
  --management-backend HOST:PORT
                          Required management/outbox backend.
  --compose-file PATH     Router Compose file.
  --backend-file PATH     Host backend.conf path.
  --env-file PATH         Explicit Compose environment file.
  --project-name NAME     Compose project name (default: cpa-router).
  --service NAME          Compose service name (default: cpa-router).
  --keepalive COUNT       Upstream keepalive pool size (default: 64).
  --apply                 Validate, replace backend.conf, and reload Nginx.
  -h, --help              Show this help.

Without --apply this command is a non-mutating dry run.
Rollback uses the same command with the prior backend as --backend.
USAGE
}

die() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

while (($# > 0)); do
    case "$1" in
        --backend)
            (($# >= 2)) || die "--backend requires a value"
            BACKEND=$2
            shift 2
            ;;
        --management-backend)
            (($# >= 2)) || die "--management-backend requires a value"
            MANAGEMENT_BACKEND=$2
            shift 2
            ;;
        --compose-file)
            (($# >= 2)) || die "--compose-file requires a value"
            COMPOSE_FILE=$2
            shift 2
            ;;
        --backend-file)
            (($# >= 2)) || die "--backend-file requires a value"
            BACKEND_FILE=$2
            shift 2
            ;;
        --env-file)
            (($# >= 2)) || die "--env-file requires a value"
            ENV_FILE=$2
            shift 2
            ;;
        --project-name)
            (($# >= 2)) || die "--project-name requires a value"
            PROJECT_NAME=$2
            shift 2
            ;;
        --service)
            (($# >= 2)) || die "--service requires a value"
            SERVICE_NAME=$2
            shift 2
            ;;
        --keepalive)
            (($# >= 2)) || die "--keepalive requires a value"
            KEEPALIVE=$2
            shift 2
            ;;
        --apply)
            APPLY=1
            shift
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

[[ -n "$BACKEND" ]] || die "--backend is required"
[[ -n "$MANAGEMENT_BACKEND" ]] || die "--management-backend is required"
[[ "$BACKEND" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*:([0-9]{1,5})$ ]] || die "backend must be HOST:PORT using a DNS name or IPv4 address"
BACKEND_PORT=${BASH_REMATCH[1]}
((BACKEND_PORT >= 1 && BACKEND_PORT <= 65535)) || die "backend port must be between 1 and 65535"
[[ "$MANAGEMENT_BACKEND" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*:([0-9]{1,5})$ ]] || die "management backend must be HOST:PORT using a DNS name or IPv4 address"
MANAGEMENT_BACKEND_PORT=${BASH_REMATCH[1]}
((MANAGEMENT_BACKEND_PORT >= 1 && MANAGEMENT_BACKEND_PORT <= 65535)) || die "management backend port must be between 1 and 65535"
[[ "$KEEPALIVE" =~ ^[0-9]+$ ]] && ((KEEPALIVE >= 1 && KEEPALIVE <= 10000)) || die "keepalive must be between 1 and 10000"
[[ "$PROJECT_NAME" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || die "invalid Compose project name"
[[ "$SERVICE_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid Compose service name"
[[ -f "$COMPOSE_FILE" ]] || die "Compose file not found: $COMPOSE_FILE"
[[ -z "$ENV_FILE" || -f "$ENV_FILE" ]] || die "environment file not found: $ENV_FILE"

compose=(docker compose)
if [[ -n "$ENV_FILE" ]]; then
    compose+=(--env-file "$ENV_FILE")
fi
compose+=(--project-name "$PROJECT_NAME" --file "$COMPOSE_FILE")

if ((APPLY == 0)); then
    printf 'DRY-RUN: backend would change to %s\n' "$BACKEND"
    printf 'DRY-RUN: management backend would change to %s\n' "$MANAGEMENT_BACKEND"
    printf 'DRY-RUN: candidate would be validated in a one-shot Compose container\n'
    printf 'DRY-RUN: %s would be replaced atomically, then nginx -t and HUP reload would run\n' "$BACKEND_FILE"
    if command -v docker >/dev/null 2>&1; then
        "${compose[@]}" config --quiet
        printf 'DRY-RUN: Compose configuration is valid\n'
    else
        printf 'DRY-RUN: docker is unavailable; Compose validation was skipped\n' >&2
    fi
    exit 0
fi

command -v docker >/dev/null 2>&1 || die "docker is required with --apply"
"${compose[@]}" config --quiet
mkdir -p -- "$(dirname -- "$BACKEND_FILE")"

candidate=$(mktemp "${BACKEND_FILE}.candidate.XXXXXX")
backup=$(mktemp "${BACKEND_FILE}.backup.XXXXXX")
lock_dir="${BACKEND_FILE}.reload.lock"
had_backend=0
changed=0
lock_acquired=0

cleanup() {
    rm -f -- "$candidate" "$backup"
    if ((lock_acquired == 1)); then
        rmdir -- "$lock_dir" >/dev/null 2>&1 || true
    fi
}

finish() {
    local exit_status=$?
    trap - EXIT
    if ((changed == 1)); then
        printf 'ERROR: reload failed; restoring previous backend configuration\n' >&2
        if ((had_backend == 1)); then
            cp -p -- "$backup" "${BACKEND_FILE}.restore"
            mv -f -- "${BACKEND_FILE}.restore" "$BACKEND_FILE"
        else
            rm -f -- "$BACKEND_FILE"
        fi
        "${compose[@]}" exec -T "$SERVICE_NAME" nginx -t >/dev/null 2>&1 || true
        "${compose[@]}" exec -T "$SERVICE_NAME" nginx -s reload >/dev/null 2>&1 || true
    fi
    cleanup
    exit "$exit_status"
}

trap finish EXIT

mkdir -- "$lock_dir" 2>/dev/null || die "another backend reload is active or a stale lock exists: $lock_dir"
lock_acquired=1

cat >"$candidate" <<EOF
# Managed by scripts/cpa-router/reload-backend.sh.
upstream cpa_proxy_backend {
    server ${BACKEND};
    keepalive ${KEEPALIVE};
}

upstream cpa_management_backend {
    server ${MANAGEMENT_BACKEND};
    keepalive ${KEEPALIVE};
}
EOF
chmod 0644 "$candidate"

# Validate the candidate without modifying the running router configuration.
"${compose[@]}" run --rm --no-deps -T \
    --entrypoint nginx \
    --volume "${candidate}:/etc/nginx/conf.d/backend.conf:ro" \
    "$SERVICE_NAME" -t

if [[ -f "$BACKEND_FILE" ]]; then
    cp -p -- "$BACKEND_FILE" "$backup"
    had_backend=1
fi
mv -f -- "$candidate" "$BACKEND_FILE"
changed=1

"${compose[@]}" exec -T "$SERVICE_NAME" nginx -t
"${compose[@]}" exec -T "$SERVICE_NAME" nginx -s reload

rendered_config=$("${compose[@]}" exec -T "$SERVICE_NAME" nginx -T 2>&1)
upstream_contains() {
    local upstream_name=$1 expected=$2
    awk -v upstream_name="$upstream_name" -v expected="$expected" '
        $1 == "upstream" && $2 == upstream_name { inside = 1; next }
        inside && $1 == "}" { exit found ? 0 : 1 }
        inside && $1 == "server" {
            value = $2
            sub(/;$/, "", value)
            if (value == expected) found = 1
        }
        END { if (inside) exit found ? 0 : 1 }
    ' <<<"$rendered_config"
}
upstream_contains cpa_proxy_backend "$BACKEND" || die "reloaded proxy upstream does not contain $BACKEND"
upstream_contains cpa_management_backend "$MANAGEMENT_BACKEND" || die "reloaded management upstream does not contain $MANAGEMENT_BACKEND"
[[ "$("${compose[@]}" ps --status running --services "$SERVICE_NAME")" == "$SERVICE_NAME" ]] || die "router service is not running"

changed=0
printf 'Router backends reloaded: proxy=%s management=%s\n' "$BACKEND" "$MANAGEMENT_BACKEND"
