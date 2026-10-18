#!/usr/bin/env bash
# PUK per-node TCP Brutal sing-box canary. Does not touch existing proxy services.
# A private bundle.json is rendered by render_tcp_brutal_canary.py on this host.
set -Eeuo pipefail

SERVICE=puk-tcp-brutal-canary.service
TARGET=/etc/puk/tcp-brutal
CONFIG=$TARGET/server.json
UNIT=/etc/systemd/system/$SERVICE
LOCK=/run/lock/puk-tcp-brutal-canary.lock
PORT=14443

msg() { printf '[puk-canary] %s\n' "$*"; }
fail() { msg "ERROR: $*" >&2; exit 1; }

(( EUID == 0 )) || fail 'Root required'
[[ $# -ge 1 ]] || fail 'Usage: activate_tcp_brutal_canary.sh --check|--activate|--rollback [private-bundle.json]'
MODE=$1
case "$MODE" in
  --check|--activate|--rollback|--status) ;;
  *) fail "Unknown mode $MODE" ;;
esac

if [[ "$MODE" == --status ]]; then
  systemctl is-active "$SERVICE" || true
  if command -v brutalctl >/dev/null 2>&1; then brutalctl list || true; fi
  exit 0
fi

if [[ "$MODE" == --rollback ]]; then
  systemctl disable --now "$SERVICE" 2>/dev/null || true
  msg 'Canary stopped; original HY2, REALITY, SSH and BBR left unchanged'
  exit 0
fi

BUNDLE=$TARGET/bundle.json
if [[ $# -ge 2 ]]; then BUNDLE=$2; fi
[[ -r "$BUNDLE" ]] || fail 'Private config bundle unavailable; run on-node renderer first'
[[ ! -L "$BUNDLE" ]] || fail 'Bundle symlink is not permitted'
command -v sing-box >/dev/null 2>&1 || fail 'sing-box not installed'
command -v python3 >/dev/null 2>&1 || fail 'python3 not installed'
id sing-box >/dev/null 2>&1 || fail 'sing-box service account missing'
[[ -r /proc/sys/net/ipv4/tcp_available_congestion_control ]] || fail 'Linux TCP CC status unavailable'
grep -qw brutal /proc/sys/net/ipv4/tcp_available_congestion_control ||
  fail 'brutal kernel module not loaded; run install_tcp_brutal_v2.sh --install'
[[ $(sysctl -n net.ipv4.tcp_congestion_control) != brutal ]] ||
  fail 'Global congestion control is brutal, not supported'
[[ -r /etc/sing-box/tls/fullchain.pem && -r /etc/sing-box/tls/privkey.pem ]] ||
  fail 'TLS certificates are not staged'

# Validate the rendered private JSON envelope before promoting only server_candidate.
CANDIDATE=$(mktemp /tmp/puk-brutal-canary.XXXXXXXX.json)
cleanup() { rm -f "$CANDIDATE"; }
trap cleanup EXIT
python3 - "$BUNDLE" "$CANDIDATE" "$PORT" <<'PY'
import json, os, sys
from pathlib import Path

source = Path(sys.argv[1])
dest = Path(sys.argv[2])
port = int(sys.argv[3])
data = json.loads(source.read_text(encoding='utf-8'))
server = data.get('server_candidate')
if not isinstance(server, dict):
    raise SystemExit('Missing candidate server config')
inbounds = server.get('inbounds')
if not isinstance(inbounds, list) or len(inbounds) != 1:
    raise SystemExit('Expected exactly one standalone inbound')
inbound = inbounds[0]
if inbound.get('type') != 'vless' or inbound.get('listen_port') != port:
    raise SystemExit('Unexpected protocol or port; refusing installation')
if inbound.get('listen') not in ('::', '0.0.0.0'):
    raise SystemExit('Expected an explicit IPv4 or IPv6 wildcard bind')
by_user = data.get('client_canary_outbounds_by_user', {})
if inbound['listen'] == '0.0.0.0':
    for families in by_user.values():
        if 'v6' in families or 'dual' in families:
            raise SystemExit('IPv4-only listener conflicts with v6/dual exports')
tls = inbound.get('tls', {})
if tls.get('enabled') is not True or tls.get('insecure') is True:
    raise SystemExit('Cannot enable an insecure or missing TLS listener')
if tls.get('certificate_path') != '/etc/sing-box/tls/fullchain.pem':
    raise SystemExit('Wrong certificate chain location')
if tls.get('key_path') != '/etc/sing-box/tls/privkey.pem':
    raise SystemExit('Wrong private key location')
mux = inbound.get('multiplex', {})
brutal = mux.get('brutal', {})
if mux.get('enabled') is not True or brutal.get('enabled') is not True:
    raise SystemExit('Not a multiplex TCP Brutal candidate')
if not all(isinstance(brutal.get(k), int) and not isinstance(brutal.get(k), bool)
           and 1 <= brutal[k] <= 100000 for k in ('up_mbps', 'down_mbps')):
    raise SystemExit('Brutal rate invalid')
if any(user.get('flow') for user in inbound.get('users', [])):
    raise SystemExit('Flow + multiplex is incompatible; keep Reality+Vision separate')
outbounds = server.get('outbounds', [])
if len(outbounds) != 1 or outbounds[0].get('type') != 'direct':
    raise SystemExit('This canary permits only a real Direct outbound')
if server.get('route', {}).get('final') != outbounds[0].get('tag'):
    raise SystemExit('Canary route target mismatch')
dest.write_text(json.dumps(server, indent=2) + '\n', encoding='utf-8')
os.chmod(dest, 0o600)
print('Private candidate parsed: 1 inbound; TLS; mux Brutal; Direct; no flow')
PY
# A true v4/v6 dual listener requires mapped IPv4 support. Abort rather than
# claim a dual-family edge that only accepts IPv6.
if grep -q '"dual"' "$BUNDLE" && grep -q '"listen": "::"' "$CANDIDATE"; then
  [[ $(sysctl -n net.ipv6.bindv6only 2>/dev/null) == 0 ]] ||
    fail 'IPv6 wildcard is v6only: dual family cannot be advertised'
fi
sing-box check -c "$CANDIDATE" >/dev/null ||
  fail 'sing-box syntax or Brutal support check failed'
msg "Config check passed; active CC=$(sysctl -n net.ipv4.tcp_congestion_control)"

if [[ "$MODE" == --check ]]; then
  msg 'Dry-run complete; no services or subscriptions changed'
  exit 0
fi

command -v flock >/dev/null 2>&1 || fail 'Missing flock'
exec 9>"$LOCK"
flock -n 9 || fail 'Another activation owns this canary'
[[ ! -f "$UNIT" ]] ||
  fail 'An existing canary service unit exists; use --rollback and remove it manually after inspecting'
if command -v ss >/dev/null 2>&1; then
  if ss -H -ltn "sport = :$PORT" | grep -q .; then
    fail "TCP port $PORT already in use; not overwriting another service"
  fi
fi

install -d -o root -g sing-box -m 0750 "$TARGET"
install -o root -g sing-box -m 0640 "$CANDIDATE" "$CONFIG"
cat >"$UNIT" <<'EOF'
[Unit]
Description=PUK isolated TCP Brutal canary (no original listener changes)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=sing-box
Group=sing-box
ExecStart=/usr/bin/sing-box run -c /etc/puk/tcp-brutal/server.json
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "$UNIT"
systemctl daemon-reload
if ! systemctl enable --now "$SERVICE"; then
  systemctl disable --now "$SERVICE" 2>/dev/null || true
  rm -f "$UNIT"
  systemctl daemon-reload
  fail 'Canary failed to start; unit removed, other services untouched'
fi
sleep 2
if ! systemctl is-active --quiet "$SERVICE"; then
  systemctl disable --now "$SERVICE" 2>/dev/null || true
  rm -f "$UNIT"
  systemctl daemon-reload
  fail 'Canary exited after start; rolled back isolated service'
fi
# ACME renewal must reload the active TLS consumer after a separate hook
# has copied the renewed certificate into /etc/sing-box/tls.
# Use a distinct additive hook; do not overwrite existing renewal scripts.
if [[ -d /etc/letsencrypt/renewal-hooks/deploy ]]; then
  RENEW_HOOK=/etc/letsencrypt/renewal-hooks/deploy/88-puk-brutal-canary-reload.sh
  if [[ ! -e "$RENEW_HOOK" ]]; then
    cat > "$RENEW_HOOK" <<'HOOK'
#!/bin/sh
# Re-read TLS after the origin cert synchronization deploy hook finishes.
if systemctl is-active --quiet puk-tcp-brutal-canary.service; then
    systemctl try-restart puk-tcp-brutal-canary.service
fi
HOOK
    chmod 0755 "$RENEW_HOOK"
  fi
fi
msg 'CANARY SERVICE ACTIVE; NOT ADDED TO PUBLIC SUBSCRIPTION'
msg "Dedicated TCP listener on port $PORT; perform external application checks before region/family publication"
