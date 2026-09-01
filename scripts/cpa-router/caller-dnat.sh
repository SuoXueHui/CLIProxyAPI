#!/usr/bin/env bash
set -Eeuo pipefail

ACTION=""
CALLER=""
OLD_IP=""
ROUTER_IP=""
EXPECTED_DNS_IP=""
PORT=8317
ALIAS="cli-proxy-api"
EXPECT_RULE="present"
DNS_CHECKS=3
DNS_DELAY=2
ALLOW_ADDITIONAL_DNS=0
APPLY=0
RULE_COMMENT="cpa-router-handoff"

usage() {
    cat <<'USAGE'
Usage:
  caller-dnat.sh install --caller NAME --old-ip IPv4 --router-ip IPv4 [options]
  caller-dnat.sh remove  --caller NAME --old-ip IPv4 --router-ip IPv4 [options]
  caller-dnat.sh verify  --caller NAME --old-ip IPv4 --router-ip IPv4 [options]

Options:
  --port PORT              CPA destination port (default: 8317).
  --alias NAME             Docker DNS alias (default: cli-proxy-api).
  --expected-dns-ip IPv4   Required before remove; defaults to router IP.
                            Use the old IP when removing a rollback rule.
  --expect-rule STATE      verify expectation: present or absent.
  --dns-checks COUNT       Consecutive DNS checks before removal (default: 3).
  --dns-delay SECONDS      Delay between DNS checks (default: 2).
  --allow-additional-dns   verify only: allow alias overlap during handoff.
  --apply                  Required for install and remove mutations.
  -h, --help               Show this help.

install and remove are dry-run only without --apply. The rule affects only NEW
TCP connections; established connections keep their original conntrack path.
USAGE
}

die() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

valid_ipv4() {
    local ip=$1 octet
    local -a octets
    IFS=. read -r -a octets <<<"$ip"
    ((${#octets[@]} == 4)) || return 1
    for octet in "${octets[@]}"; do
        [[ "$octet" =~ ^[0-9]{1,3}$ ]] || return 1
        ((10#$octet <= 255)) || return 1
    done
}

if (($# == 1)) && [[ "$1" == "-h" || "$1" == "--help" ]]; then
    usage
    exit 0
fi

(($# > 0)) || {
    usage
    exit 2
}
ACTION=$1
shift

while (($# > 0)); do
    case "$1" in
        --caller)
            (($# >= 2)) || die "--caller requires a value"
            CALLER=$2
            shift 2
            ;;
        --old-ip)
            (($# >= 2)) || die "--old-ip requires a value"
            OLD_IP=$2
            shift 2
            ;;
        --router-ip)
            (($# >= 2)) || die "--router-ip requires a value"
            ROUTER_IP=$2
            shift 2
            ;;
        --expected-dns-ip)
            (($# >= 2)) || die "--expected-dns-ip requires a value"
            EXPECTED_DNS_IP=$2
            shift 2
            ;;
        --port)
            (($# >= 2)) || die "--port requires a value"
            PORT=$2
            shift 2
            ;;
        --alias)
            (($# >= 2)) || die "--alias requires a value"
            ALIAS=$2
            shift 2
            ;;
        --expect-rule)
            (($# >= 2)) || die "--expect-rule requires a value"
            EXPECT_RULE=$2
            shift 2
            ;;
        --dns-checks)
            (($# >= 2)) || die "--dns-checks requires a value"
            DNS_CHECKS=$2
            shift 2
            ;;
        --dns-delay)
            (($# >= 2)) || die "--dns-delay requires a value"
            DNS_DELAY=$2
            shift 2
            ;;
        --allow-additional-dns)
            ALLOW_ADDITIONAL_DNS=1
            shift
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

[[ "$ACTION" == "install" || "$ACTION" == "remove" || "$ACTION" == "verify" ]] || die "action must be install, remove, or verify"
[[ "$CALLER" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "--caller must be a container name or ID"
valid_ipv4 "$OLD_IP" || die "--old-ip must be an exact IPv4 address"
valid_ipv4 "$ROUTER_IP" || die "--router-ip must be an exact IPv4 address"
[[ "$OLD_IP" != "$ROUTER_IP" ]] || die "old and router IPs must differ"
[[ "$PORT" =~ ^[0-9]+$ ]] && ((PORT >= 1 && PORT <= 65535)) || die "port must be between 1 and 65535"
[[ "$ALIAS" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "invalid DNS alias"
[[ "$EXPECT_RULE" == "present" || "$EXPECT_RULE" == "absent" ]] || die "--expect-rule must be present or absent"
[[ "$DNS_CHECKS" =~ ^[0-9]+$ ]] && ((DNS_CHECKS >= 1 && DNS_CHECKS <= 20)) || die "dns-checks must be between 1 and 20"
[[ "$DNS_DELAY" =~ ^[0-9]+$ ]] && ((DNS_DELAY <= 60)) || die "dns-delay must be between 0 and 60"
if ((ALLOW_ADDITIONAL_DNS == 1)) && [[ "$ACTION" != "verify" ]]; then
    die "--allow-additional-dns is valid only for verify"
fi

if [[ -z "$EXPECTED_DNS_IP" ]]; then
    EXPECTED_DNS_IP=$ROUTER_IP
fi
valid_ipv4 "$EXPECTED_DNS_IP" || die "--expected-dns-ip must be an exact IPv4 address"

if [[ "$ACTION" != "verify" && "$APPLY" == 0 ]]; then
    printf 'DRY-RUN: would %s a NEW-connection DNAT rule in caller %s\n' "$ACTION" "$CALLER"
    printf 'DRY-RUN: %s:%s -> %s:%s, comment=%s\n' "$OLD_IP" "$PORT" "$ROUTER_IP" "$PORT" "$RULE_COMMENT"
    if [[ "$ACTION" == "remove" ]]; then
        printf 'DRY-RUN: removal would require %s DNS checks resolving %s to %s and zero old established connections\n' \
            "$DNS_CHECKS" "$ALIAS" "$EXPECTED_DNS_IP"
    fi
    exit 0
fi

((EUID == 0)) || die "verify and --apply operations must run as root"
for command_name in docker nsenter iptables conntrack curl; do
    command -v "$command_name" >/dev/null 2>&1 || die "required command not found: $command_name"
done

running=$(docker inspect --format '{{.State.Running}}' "$CALLER" 2>/dev/null) || die "caller container not found: $CALLER"
[[ "$running" == "true" ]] || die "caller container is not running: $CALLER"
pid=$(docker inspect --format '{{.State.Pid}}' "$CALLER")
[[ "$pid" =~ ^[1-9][0-9]*$ ]] || die "invalid caller PID"

rule=(iptables -w 5 -t nat)
rule_spec=(OUTPUT -p tcp -d "$OLD_IP" --dport "$PORT" -m conntrack --ctstate NEW -m comment --comment "$RULE_COMMENT" -j DNAT --to-destination "${ROUTER_IP}:${PORT}")

rule_exists() {
    nsenter --target "$pid" --net -- "${rule[@]}" -C "${rule_spec[@]}" >/dev/null 2>&1
}

probe_router() {
    nsenter --target "$pid" --net -- curl --fail --silent --show-error \
        --connect-timeout 3 --max-time 5 \
        "http://${ROUTER_IP}:${PORT}/cpa-router-health" | grep -qx 'cpa-router ok'
}

probe_via_old_path() {
    nsenter --target "$pid" --net -- curl --fail --silent --show-error \
        --connect-timeout 3 --max-time 5 \
        "http://${OLD_IP}:${PORT}/cpa-router-health" | grep -qx 'cpa-router ok'
}

old_connection_count() {
    # A DNAT connection has router_ip as the reply tuple source. Only a reply
    # source equal to old_ip is still using the direct legacy path.
    nsenter --target "$pid" --net -- conntrack -L -p tcp 2>/dev/null | \
        awk -v old_ip="$OLD_IP" -v port="$PORT" '
            index($0, "ESTABLISHED") == 0 { next }
            {
                original_dst = ""
                original_dport = ""
                reply_src = ""
                src_count = 0
                dst_count = 0
                dport_count = 0
                for (field = 1; field <= NF; field++) {
                    if ($field ~ /^src=/) {
                        src_count++
                        if (src_count == 2) {
                            reply_src = substr($field, 5)
                        }
                    } else if ($field ~ /^dst=/) {
                        dst_count++
                        if (dst_count == 1) {
                            original_dst = substr($field, 5)
                        }
                    } else if ($field ~ /^dport=/) {
                        dport_count++
                        if (dport_count == 1) {
                            original_dport = substr($field, 7)
                        }
                    }
                }
                if (original_dst == old_ip && original_dport == port && reply_src == old_ip) {
                    count++
                }
            }
            END { print count + 0 }
        '
}

check_dns_once() {
    local dns_output
    dns_output=$(docker exec "$CALLER" getent ahostsv4 "$ALIAS" 2>/dev/null) || return 1
    awk -v expected="$EXPECTED_DNS_IP" -v allow_additional="$ALLOW_ADDITIONAL_DNS" '
        $1 == expected { found = 1; next }
        NF > 0 { unexpected = 1 }
        END { exit !(found && (allow_additional || !unexpected)) }
    ' <<<"$dns_output"
}

check_dns_consistently() {
    local check
    for ((check = 1; check <= DNS_CHECKS; check++)); do
        check_dns_once || return 1
        if ((check < DNS_CHECKS)); then
            sleep "$DNS_DELAY"
        fi
    done
}

case "$ACTION" in
    install)
        probe_router || die "router health probe failed from caller namespace"
        if rule_exists; then
            probe_via_old_path || die "existing DNAT rule does not send the old destination to router health"
            printf 'DNAT rule already present for caller %s\n' "$CALLER"
            exit 0
        fi
        nsenter --target "$pid" --net -- "${rule[@]}" -I "${rule_spec[@]}"
        rule_exists || die "DNAT rule was not installed"
        if ! probe_via_old_path; then
            nsenter --target "$pid" --net -- "${rule[@]}" -D "${rule_spec[@]}" >/dev/null 2>&1 || true
            die "old destination did not reach router health after DNAT; rule was rolled back"
        fi
        printf 'DNAT rule installed for caller %s\n' "$CALLER"
        ;;
    remove)
        probe_router || die "router health probe failed from caller namespace"
        check_dns_consistently || die "caller DNS did not consistently resolve $ALIAS to $EXPECTED_DNS_IP"
        connection_count=$(old_connection_count)
        ((connection_count == 0)) || die "caller still has $connection_count established connection(s) to the old CPA path"
        if ! rule_exists; then
            printf 'DNAT rule already absent for caller %s\n' "$CALLER"
            exit 0
        fi
        nsenter --target "$pid" --net -- "${rule[@]}" -D "${rule_spec[@]}"
        ! rule_exists || die "DNAT rule was not removed"
        printf 'DNAT rule removed for caller %s\n' "$CALLER"
        ;;
    verify)
        probe_router || die "router health probe failed from caller namespace"
        check_dns_consistently || die "caller DNS did not consistently resolve $ALIAS to $EXPECTED_DNS_IP"
        connection_count=$(old_connection_count)
        if [[ "$EXPECT_RULE" == "present" ]]; then
            rule_exists || die "expected DNAT rule is absent"
            probe_via_old_path || die "old destination did not reach router health through DNAT"
        else
            ! rule_exists || die "expected DNAT rule is still present"
        fi
        ((connection_count == 0)) || die "caller still has $connection_count established connection(s) to the old CPA path"
        printf 'Caller verification passed: caller=%s rule=%s old-established=%s dns=%s\n' \
            "$CALLER" "$EXPECT_RULE" "$connection_count" "$EXPECTED_DNS_IP"
        ;;
esac
