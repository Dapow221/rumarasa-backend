#!/usr/bin/env bash
# Open TCP 80/443 in the OCI security list for the instance's subnet.
#
# NOTE: this is only half the job. Oracle's Ubuntu images ALSO ship a local
# iptables ruleset that rejects everything except port 22 — bootstrap.sh
# handles that side. Both layers must be open or you'll get silent timeouts.
set -euo pipefail

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
err() { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; }

COMPARTMENT="${COMPARTMENT:-$(grep -E '^tenancy' ~/.oci/config | head -1 | cut -d= -f2 | tr -d ' ')}"
[[ -n "$COMPARTMENT" ]] || { err "Could not determine compartment"; exit 1; }

SUBNET="${SUBNET:-$(oci network subnet list -c "$COMPARTMENT" \
  --query 'data[?"prohibit-public-ip-on-vnic"==`false`].id | [0]' --raw-output)}"
[[ -n "$SUBNET" && "$SUBNET" != "null" ]] || { err "No public subnet found"; exit 1; }

SL=$(oci network subnet get --subnet-id "$SUBNET" \
  --query 'data."security-list-ids"[0]' --raw-output)
[[ -n "$SL" && "$SL" != "null" ]] || { err "No security list on subnet"; exit 1; }
log "Security list: $SL"

CURRENT=$(oci network security-list get --security-list-id "$SL" \
  --query 'data."ingress-security-rules"' --raw-output)

MERGED=$(python3 - "$CURRENT" <<'PY'
import json, sys

rules = json.loads(sys.argv[1])

def has_port(rules, port):
    for r in rules:
        if r.get("protocol") != "6":
            continue
        opts = r.get("tcp-options") or {}
        dst = opts.get("destination-port-range") or {}
        # a TCP rule with no port range means "all TCP ports"
        if not dst:
            return True
        if dst.get("min", 0) <= port <= dst.get("max", 65535):
            return True
    return False

for port in (80, 443):
    if has_port(rules, port):
        print(f"port {port} already open", file=sys.stderr)
        continue
    rules.append({
        "protocol": "6",
        "source": "0.0.0.0/0",
        "source-type": "CIDR_BLOCK",
        "is-stateless": False,
        "description": f"HTTP/S {port}",
        "tcp-options": {"destination-port-range": {"min": port, "max": port}},
    })
    print(f"adding port {port}", file=sys.stderr)

print(json.dumps(rules))
PY
)

oci network security-list update --security-list-id "$SL" \
  --ingress-security-rules "$MERGED" --force >/dev/null

log "Ingress rules updated — 80/443 open at the OCI layer."
