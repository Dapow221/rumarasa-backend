# Oracle Cloud ARM deployment

Deploys the Rumarasa API to an Oracle Cloud **Always Free** ARM instance
(VM.Standard.A1.Flex — 4 OCPU / 24 GB RAM / 100 GB disk, permanently free).

Target layout on the box:

```
Caddy :80/:443  ──TLS──▶  rumarasa-api :8080  ──▶  PostgreSQL :5432 (localhost only)
   (systemd)               (systemd, user=rumarasa)
```

---

## Step 1 — Create the Oracle Cloud account (manual)

https://signup.cloud.oracle.com/

| Field | Value |
|---|---|
| Account type | **Personal** |
| Country / address | Your **real** address — must match the card's billing address |
| Mobile | Your **real** number — receives the SMS code |
| **Home Region** | `Singapore (ap-singapore-1)` or `Osaka (ap-osaka-1)` |

- **Home region is permanent** and unrelated to where you live. It only picks the
  datacenter. Singapore is closest to Indonesia but is the most contended for free
  ARM capacity; Osaka is ~30 ms slower and usually has capacity available.
- The card is for identity verification. Always Free resources are never charged.
- **Do not click "Upgrade to Paid Account"** — that ends the free tier.
- Sign up from a normal residential connection. VPN signups are frequently auto-rejected.

## Step 2 — Set up API credentials

In the console: profile icon (top right) → **My profile** → **API keys** → **Add API key**
→ *Generate API key pair* → download the private key → **Add**.

Oracle then shows a config preview. Copy it, then:

```bash
mkdir -p ~/.oci && chmod 700 ~/.oci
mv ~/Downloads/*.pem ~/.oci/oci_api_key.pem
chmod 600 ~/.oci/oci_api_key.pem
$EDITOR ~/.oci/config     # paste the preview; set key_file=~/.oci/oci_api_key.pem
oci iam region list       # verify — should print a region table
```

## Step 3 — Create the network (one-time, console)

**Networking → Virtual Cloud Networks → Start VCN Wizard →
"Create VCN with Internet Connectivity"** → accept defaults → Create.

This is two clicks in the console versus ~6 chained CLI calls, so it isn't scripted.

## Step 4 — Provision the instance

```bash
./provision.sh
```

Free ARM capacity is scarce — launches fail with *"Out of host capacity"* for hours
or days. The script sweeps every availability domain on a 90 s loop until one lands.
Leave it running.

```bash
OCPUS=1 MEMORY_GB=6 ./provision.sh    # smaller shapes land sooner
DISPLAY_NAME=api-2 ./provision.sh     # name it something else
```

The free allowance is **4 OCPU / 24 GB total** across all A1 instances — one big box
or up to four small ones.

## Step 5 — Open the firewall

```bash
./open-ports.sh
```

There are **two** firewalls and both must be open:
1. The OCI security list (this script)
2. The instance's local iptables — Oracle's Ubuntu image rejects everything but
   port 22 (handled by `bootstrap.sh`)

Forgetting either one produces a silent connection timeout with no error anywhere.

## Step 6 — Point DNS

Create an `A` record for your domain → the instance's public IP. Let it propagate
before the next step; Caddy needs it resolving to issue the Let's Encrypt cert.

## Step 7 — Bootstrap the server

```bash
./bootstrap.sh <public-ip> api.yourdomain.com
```

Installs PostgreSQL 16, creates the `rumarasa` role/database with a random password,
generates `.env` (random `JWT_SECRET`, random bootstrap admin password), installs the
systemd unit, and configures Caddy with automatic TLS.

**It prints the bootstrap admin password exactly once.** Save it and change it after
first login. Idempotent — re-running won't clobber an existing `.env` or database.

## Step 8 — Deploy

```bash
./deploy.sh <public-ip>
```

Cross-compiles `linux/arm64` on your Mac (`CGO_ENABLED=0`, ~10 MB static binary —
pgx is pure Go, so no toolchain is needed on the server), uploads, swaps the binary,
restarts, and verifies the service came up. On failure it prints the last 30 journal
lines and exits non-zero.

---

## Operations

```bash
ssh ubuntu@<ip> 'journalctl -u rumarasa -f'          # tail logs
ssh ubuntu@<ip> 'sudo systemctl restart rumarasa'    # restart
ssh ubuntu@<ip> 'sudo -u postgres psql rumarasa'     # psql
ssh ubuntu@<ip> 'sudo cat /opt/rumarasa/.env'        # env (contains secrets)
```

## Gotchas

- **Idle reclamation.** Oracle reclaims Always Free instances idle for 7 days
  (< 10% CPU, < 20% network). A live API with health checks generally stays under
  the threshold anyway — if you get a reclamation warning email, that's why.
- **Two firewalls.** See step 5. This causes most "why is my port closed" confusion.
- **Do not upgrade to paid** to fix a capacity error. It won't help; A1 capacity is
  constrained for paid tenancies too.
- **Backups.** Nothing here backs up Postgres. Add a `pg_dump` cron before you have
  data you care about.
- **PG version skew.** Ubuntu 24.04 ships PostgreSQL 16. Dumps from an older cluster
  (e.g. the PG12 issue hit during the wisphers migration) need `pg_dump` run by the
  *newer* client, or restore fails on unrecognised options.
