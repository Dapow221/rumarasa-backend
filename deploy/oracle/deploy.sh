#!/usr/bin/env bash
# Cross-compile the API for linux/arm64 and ship it.
#   ./deploy.sh <public-ip>
set -euo pipefail

IP="${1:?usage: ./deploy.sh <public-ip>}"
SSH_USER="${SSH_USER:-ubuntu}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="$(mktemp -d)/rumarasa-api"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

log "Building linux/arm64 binary"
cd "$REPO_ROOT"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w" -o "$BIN" ./cmd/api
log "Built $(du -h "$BIN" | cut -f1)"

log "Uploading"
scp -q "$BIN" "$SSH_USER@$IP:/tmp/rumarasa-api.new"

log "Swapping binary and restarting"
ssh "$SSH_USER@$IP" 'bash -seu' <<'REMOTE'
sudo install -o rumarasa -g rumarasa -m 0755 /tmp/rumarasa-api.new /opt/rumarasa/rumarasa-api
rm -f /tmp/rumarasa-api.new
sudo systemctl restart rumarasa
sleep 2
if systemctl is-active --quiet rumarasa; then
  echo "  service is running"
else
  echo "  SERVICE FAILED TO START — last 30 log lines:" >&2
  sudo journalctl -u rumarasa -n 30 --no-pager >&2
  exit 1
fi
REMOTE

log "Deployed. Logs: ssh $SSH_USER@$IP 'journalctl -u rumarasa -f'"
