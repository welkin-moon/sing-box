#!/usr/bin/env bash
# Install TCP Brutal v2 + a separate sing-box canary on one Debian VPS.
# Never rewrites existing HY2/REALITY, proxy subscribers, system routes or BBR.
set -Eeuo pipefail

BASE=$(cd -- "$(dirname -- "$0")" && pwd)
MODE=check
REGION=
SNI=
HOSTNAMES=
IDENTITIES=
CERT=/etc/sing-box/tls
PORT=14443
UP=
DOWN=
STATE=/etc/puk/tcp-brutal

usage() {
  printf '%s\n' 'Usage: deploy_tcp_brutal_region.sh --check|--stage|--deploy --region NAME --tls-sni HOST --identity-file PRIVATE_JSON --hostnames-file FAMILY_JSON --up-mbps N --down-mbps N'
  exit 2
}
while (( $# > 0 )); do
  case "$1" in
    --check) MODE=check; shift ;;
    --stage) MODE=stage; shift ;;
    --deploy) MODE=deploy; shift ;;
    --region) REGION=$2; shift 2 ;;
    --tls-sni) SNI=$2; shift 2 ;;
    --identity-file) IDENTITIES=$2; shift 2 ;;
    --hostnames-file) HOSTNAMES=$2; shift 2 ;;
    --up-mbps) UP=$2; shift 2 ;;
    --down-mbps) DOWN=$2; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$REGION" && -n "$SNI" && -n "$IDENTITIES" && -n "$HOSTNAMES" && -n "$UP" && -n "$DOWN" ]] ||
  usage
[[ $(id -u) == 0 ]] || { echo 'Root required' >&2; exit 2; }
[[ -f "$IDENTITIES" && -f "$HOSTNAMES" ]] || { echo 'Missing private on-host manifests' >&2; exit 2; }
[[ -f "$BASE/install_tcp_brutal_v2.sh" && -f "$BASE/activate_tcp_brutal_canary.sh" && -f "$BASE/export_tcp_brutal_subscribers.py" ]] ||
  { echo 'Incomplete deploy bundle' >&2; exit 2; }

# Refuse to overwrite an existing service. Roll back with the explicit command
# and inspect ownership before reactivating.
if systemctl cat puk-tcp-brutal-canary.service >/dev/null 2>&1; then
  echo 'An existing TCP Brutal canary unit already exists; inspect first' >&2
  exit 3
fi

bash "$BASE/install_tcp_brutal_v2.sh" --check
[[ -d "$CERT" ]] || { echo 'Certificate staging directory missing' >&2; exit 3; }
[[ -r "$CERT/fullchain.pem" && -r "$CERT/privkey.pem" ]] ||
  { echo 'TLS certificate/key not staged' >&2; exit 3; }
if [[ "$MODE" == check ]]; then
  echo 'Read-only preflight complete: manifests, certificates, module environment inspected.'
  exit 0
fi
if [[ "$MODE" == deploy ]]; then
  bash "$BASE/install_tcp_brutal_v2.sh" --install
fi

# Run render under root and never print credential values.
install -d -o root -g sing-box -m 0750 "$STATE"
BUNDLE="$STATE/bundle.json"
python3 "$BASE/render_tcp_brutal_canary.py" \
  --identity-file "$IDENTITIES" --hostnames-file "$HOSTNAMES" \
  --cert-dir "$CERT" --region "$REGION" --tls-sni "$SNI" \
  --port "$PORT" --up-mbps "$UP" --down-mbps "$DOWN" \
  --output "$BUNDLE"
chmod 0600 "$BUNDLE"
python3 "$BASE/export_tcp_brutal_subscribers.py" \
  --bundle "$BUNDLE" --out-dir "$STATE/subscribers"
echo 'Rendered root-only per-user candidate JSON; do not upload these files to GitHub.'
if [[ "$MODE" == stage ]]; then
  if grep -qw brutal /proc/sys/net/ipv4/tcp_available_congestion_control; then
    bash "$BASE/activate_tcp_brutal_canary.sh" --check "$BUNDLE"
  else
    echo 'Module absent: candidate staged only. Run --deploy after matching kernel headers are installed.'
  fi
  exit 0
fi
bash "$BASE/activate_tcp_brutal_canary.sh" --activate "$BUNDLE"
echo 'Activation successful. Do not publish V4/V6/Dual/WARP names before their real origin and egress acceptance.'
