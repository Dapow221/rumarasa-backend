#!/usr/bin/env bash
# Provision an Oracle Cloud Always Free ARM instance (VM.Standard.A1.Flex).
#
# The Always Free ARM pool is heavily contended: launches fail with
# "Out of host capacity" for hours or days at a time. This script retries
# across every availability domain on a backoff loop until one lands.
#
# Prereqs: oci CLI configured (`oci setup config`), and a VCN with a public
# subnet already present in the compartment (see README step 3).
set -euo pipefail

DISPLAY_NAME="${DISPLAY_NAME:-rumarasa-arm}"
OCPUS="${OCPUS:-4}"
MEMORY_GB="${MEMORY_GB:-24}"
BOOT_VOLUME_GB="${BOOT_VOLUME_GB:-100}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519.pub}"
UBUNTU_VERSION="${UBUNTU_VERSION:-24.04}"
RETRY_INTERVAL="${RETRY_INTERVAL:-90}"   # seconds between full AD sweeps
MAX_ATTEMPTS="${MAX_ATTEMPTS:-0}"        # 0 = retry forever

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
err() { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; }

[[ -f "$SSH_KEY" ]] || { err "SSH public key not found: $SSH_KEY"; exit 1; }
command -v oci >/dev/null || { err "oci CLI not installed"; exit 1; }

# --- Discover tenancy / compartment -----------------------------------------
COMPARTMENT="${COMPARTMENT:-$(oci iam compartment list --query 'data[0]."compartment-id"' --raw-output 2>/dev/null || true)}"
if [[ -z "${COMPARTMENT:-}" || "$COMPARTMENT" == "null" ]]; then
  COMPARTMENT=$(grep -E '^tenancy' ~/.oci/config | head -1 | cut -d= -f2 | tr -d ' ')
fi
[[ -n "$COMPARTMENT" ]] || { err "Could not determine compartment OCID"; exit 1; }
log "Compartment: $COMPARTMENT"

# --- Discover a public subnet ------------------------------------------------
SUBNET="${SUBNET:-$(oci network subnet list -c "$COMPARTMENT" \
  --query 'data[?"prohibit-public-ip-on-vnic"==`false`].id | [0]' --raw-output 2>/dev/null || true)}"
if [[ -z "${SUBNET:-}" || "$SUBNET" == "null" ]]; then
  err "No public subnet found. In the OCI console: Networking > Virtual Cloud Networks"
  err "> 'Start VCN Wizard' > 'Create VCN with Internet Connectivity'. Then re-run."
  exit 1
fi
log "Subnet: $SUBNET"

# --- Discover newest Ubuntu ARM image ---------------------------------------
IMAGE="${IMAGE:-$(oci compute image list -c "$COMPARTMENT" \
  --operating-system "Canonical Ubuntu" \
  --operating-system-version "$UBUNTU_VERSION" \
  --shape "VM.Standard.A1.Flex" \
  --sort-by TIMECREATED --sort-order DESC \
  --query 'data[0].id' --raw-output 2>/dev/null || true)}"
[[ -n "${IMAGE:-}" && "$IMAGE" != "null" ]] || { err "No Ubuntu $UBUNTU_VERSION ARM image found"; exit 1; }
log "Image:  $IMAGE"

# --- Availability domains ----------------------------------------------------
# No mapfile here: macOS ships bash 3.2, which doesn't have it.
ADS=()
while IFS= read -r ad; do
  [[ -n "$ad" ]] && ADS+=("$ad")
done < <(oci iam availability-domain list -c "$COMPARTMENT" \
           | python3 -c 'import json,sys; [print(d["name"]) for d in json.load(sys.stdin)["data"]]')
[[ ${#ADS[@]} -gt 0 ]] || { err "No availability domains found"; exit 1; }
log "Availability domains: ${ADS[*]}"

log "Launching ${OCPUS} OCPU / ${MEMORY_GB}GB ARM instance '$DISPLAY_NAME'"
log "Capacity is scarce — this loop retries until it succeeds. Ctrl-C to stop."

attempt=0
while :; do
  attempt=$((attempt + 1))
  for ad in "${ADS[@]}"; do
    printf '  [attempt %d] %s ... ' "$attempt" "$ad"
    if out=$(oci compute instance launch \
        --availability-domain "$ad" \
        --compartment-id "$COMPARTMENT" \
        --shape "VM.Standard.A1.Flex" \
        --shape-config "{\"ocpus\":${OCPUS},\"memoryInGBs\":${MEMORY_GB}}" \
        --image-id "$IMAGE" \
        --subnet-id "$SUBNET" \
        --boot-volume-size-in-gbs "$BOOT_VOLUME_GB" \
        --assign-public-ip true \
        --display-name "$DISPLAY_NAME" \
        --ssh-authorized-keys-file "$SSH_KEY" \
        --wait-for-state RUNNING 2>&1); then
      echo "LAUNCHED"
      iid=$(echo "$out" | grep -oE '"id": "ocid1\.instance[^"]*"' | head -1 | cut -d'"' -f4)
      ip=$(oci compute instance list-vnics --instance-id "$iid" --query 'data[0]."public-ip"' --raw-output)
      log "Instance OCID: $iid"
      log "Public IP:     $ip"
      echo
      echo "Next:"
      echo "  1. ./open-ports.sh $ip     # open 80/443 in the OCI security list"
      echo "  2. ssh ubuntu@$ip          # confirm access"
      echo "  3. ./bootstrap.sh $ip <domain> # install Postgres + Caddy + service"
      exit 0
    fi

    if grep -qi "out of host capacity\|outofcapacity" <<<"$out"; then
      echo "no capacity"
    elif grep -qi "limitexceeded\|servicelimit" <<<"$out"; then
      echo "LIMIT EXCEEDED"
      err "Your tenancy limit is exhausted. Free tier allows 4 OCPU / 24GB total across"
      err "all A1 instances — destroy an existing one or lower OCPUS/MEMORY_GB."
      exit 1
    elif grep -qi "notauthenticated\|notauthorized" <<<"$out"; then
      echo "AUTH FAILED"
      err "Check ~/.oci/config and that your API key is uploaded to the console."
      exit 1
    else
      echo "failed"
      echo "$out" | head -5 >&2
    fi
  done

  if [[ "$MAX_ATTEMPTS" -gt 0 && "$attempt" -ge "$MAX_ATTEMPTS" ]]; then
    err "Gave up after $attempt attempts."
    exit 1
  fi
  sleep "$RETRY_INTERVAL"
done
