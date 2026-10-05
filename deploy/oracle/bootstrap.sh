#!/usr/bin/env bash
# One-time server setup for a fresh Oracle ARM instance.
#   ./bootstrap.sh <public-ip> <domain>
#
# Installs: PostgreSQL 16, Caddy (auto-TLS), a dedicated service user, and a
# system-level systemd unit for the API. Idempotent — safe to re-run.
#
# Deliberately uses system-level systemd rather than the `systemd --user` +
# loginctl-linger setup on wisphers: no linger to forget, starts on boot
# unconditionally, and journalctl works without --user.
set -euo pipefail

IP="${1:?usage: ./bootstrap.sh <public-ip> <domain>}"
DOMAIN="${2:?usage: ./bootstrap.sh <public-ip> <domain>}"
SSH_USER="${SSH_USER:-ubuntu}"
APP_PORT="${APP_PORT:-8080}"
CORS_ORIGINS="${CORS_ORIGINS:-https://$DOMAIN}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

log "Bootstrapping $SSH_USER@$IP for $DOMAIN"

ssh -o StrictHostKeyChecking=accept-new "$SSH_USER@$IP" \
  APP_PORT="$APP_PORT" DOMAIN="$DOMAIN" CORS_ORIGINS="$CORS_ORIGINS" 'bash -seu' <<'REMOTE'
export DEBIAN_FRONTEND=noninteractive
log() { printf '\033[1;32m  [remote]\033[0m %s\n' "$*"; }

# --- 1. Local firewall ------------------------------------------------------
# Oracle's Ubuntu image ships an iptables INPUT chain that REJECTs everything
# except 22. Without this, 80/443 time out even with the security list open.
log "Opening 80/443 in iptables"
sudo apt-get update -qq
sudo apt-get install -y -qq iptables-persistent >/dev/null
for port in 80 443; do
  if ! sudo iptables -C INPUT -p tcp --dport "$port" -j ACCEPT 2>/dev/null; then
    sudo iptables -I INPUT -p tcp --dport "$port" -j ACCEPT
  fi
done
sudo netfilter-persistent save >/dev/null

# --- 2. PostgreSQL ----------------------------------------------------------
log "Installing PostgreSQL"
sudo apt-get install -y -qq postgresql postgresql-contrib >/dev/null

if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='rumarasa'" | grep -q 1; then
  DB_PASS=$(openssl rand -hex 24)
  sudo -u postgres psql -qc "CREATE ROLE rumarasa LOGIN PASSWORD '$DB_PASS';"
  sudo -u postgres createdb -O rumarasa rumarasa
  # Ownership up front — avoids the permission-denied-on-table mess from the
  # wisphers migration, where objects ended up owned by postgres.
  sudo -u postgres psql -qd rumarasa -c "ALTER SCHEMA public OWNER TO rumarasa;"
  echo "$DB_PASS" | sudo tee /root/.rumarasa-db-pass >/dev/null
  sudo chmod 600 /root/.rumarasa-db-pass
  log "Database + role created"
else
  DB_PASS=$(sudo cat /root/.rumarasa-db-pass)
  log "Database already exists, reusing credentials"
fi

# --- 3. Service user + layout ----------------------------------------------
id rumarasa &>/dev/null || sudo useradd --system --home /opt/rumarasa --shell /usr/sbin/nologin rumarasa
sudo mkdir -p /opt/rumarasa
sudo chown rumarasa:rumarasa /opt/rumarasa

# --- 4. Environment ---------------------------------------------------------
if ! sudo test -f /opt/rumarasa/.env; then
  log "Writing /opt/rumarasa/.env"
  JWT=$(openssl rand -hex 32)
  ADMIN_PASS=$(openssl rand -base64 18)
  sudo tee /opt/rumarasa/.env >/dev/null <<ENV
APP_ENV=prod
PORT=${APP_PORT}
DATABASE_URL=postgres://rumarasa:${DB_PASS}@localhost:5432/rumarasa?sslmode=disable
JWT_SECRET=${JWT}
CORS_ORIGINS=${CORS_ORIGINS}
ADMIN_USERNAME=admin
ADMIN_PASSWORD=${ADMIN_PASS}
ENV
  sudo chown rumarasa:rumarasa /opt/rumarasa/.env
  sudo chmod 600 /opt/rumarasa/.env
  echo
  echo "  >>> BOOTSTRAP ADMIN PASSWORD: ${ADMIN_PASS}"
  echo "  >>> Save it now — change it after first login."
  echo
else
  log ".env already present, leaving it alone"
fi

# --- 5. systemd unit --------------------------------------------------------
log "Installing systemd unit"
sudo tee /etc/systemd/system/rumarasa.service >/dev/null <<'UNIT'
[Unit]
Description=Rumarasa API
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=rumarasa
WorkingDirectory=/opt/rumarasa
EnvironmentFile=/opt/rumarasa/.env
ExecStart=/opt/rumarasa/rumarasa-api
Restart=always
RestartSec=3

# Programmer errors should crash and restart, not limp along.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/rumarasa

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable rumarasa >/dev/null 2>&1

# --- 6. Caddy ---------------------------------------------------------------
if ! command -v caddy >/dev/null; then
  log "Installing Caddy"
  sudo apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl >/dev/null
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
  sudo apt-get update -qq && sudo apt-get install -y -qq caddy >/dev/null
fi

log "Configuring Caddy for ${DOMAIN}"
sudo tee /etc/caddy/Caddyfile >/dev/null <<CADDY
${DOMAIN} {
	encode zstd gzip
	header {
		X-Content-Type-Options nosniff
		X-Frame-Options DENY
		Strict-Transport-Security "max-age=31536000; includeSubDomains"
		-Server
	}
	reverse_proxy 127.0.0.1:${APP_PORT}
}
CADDY
sudo systemctl reload caddy 2>/dev/null || sudo systemctl restart caddy

log "Bootstrap complete."
REMOTE

log "Done. Point $DOMAIN's A record at $IP, then run: ./deploy.sh $IP"
