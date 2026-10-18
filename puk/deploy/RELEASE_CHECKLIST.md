# TCP Brutal v2 rollout and rollback (2026-10-18)

This repository contains a production-capable staged deployment path. Code passing
GitHub CI must never be confused with a VPS being reachable or deployed.

## Sequence

1. Restore authorized SSH or provider console access. If remote access fails,
   stop here and do NOT publish unproven entries in the existing subscription.
2. Verify the running Linux kernel matches installed headers and the BBR
   default is unchanged. Run install_tcp_brutal_v2.sh --check (read only).
   The explicit --install action pins TCP Brutal v2.0.1 upstream commit
   d2397ff8bca04a29fd2de01cf7d2d4b825224de8 and installs it through DKMS.
3. Maintain the real per-subscriber UUID identities only on the VPS in a root
   private JSON file; do not commit active IDs or private subscription links.
   Prepare a private family-hostnames JSON map: v4, v6 and/or dual. Exclude any
   missing address family, and never label Direct as Google-WARP or All-WARP.
4. Stage a trusted origin TLS certificate for the SNI host at
   /etc/sing-box/tls/fullchain.pem and /etc/sing-box/tls/privkey.pem.
5. deploy_tcp_brutal_region.sh --stage renders a root-only bundle with per-user
   sing-box JSON outbounds; it does not start a listener. --deploy explicitly
   installs v2 and starts an isolated systemd service on TCP 14443.
6. Confirm the actual module, the dedicated listener and TCP sending with
   the Brutal controller. A listening TCP port alone does not prove Brutal.
7. Test v4/v6/dual, measured bandwidth, loss, HTTPS applications, and each
   actual egress separately. The user requested test deferral, so a canary may
   be activated earlier; general subscriber publication remains gated.
8. Integrate only a secret-protected per-user source into the current Worker,
   preserving original HY2 and REALITY sources. Only sing-box JSON can
   reliably carry Multiplex TCP Brutal settings. Plain VLESS URIs/base64 and
   some Clash readers can silently ignore those extension settings.

## Example commands on authorized JPX root session

Replace paths and rate parameters with the local approved manifest and
conservative provisional downlink capacity. These are operational examples,
not claims that any command was already run on a production VPS.

    cd /opt/puk-repo/puk/deploy
    sudo bash deploy_tcp_brutal_region.sh --check --region JPX --tls-sni jpx.20100823.xyz --identity-file /etc/jpx/vless-identities.json --hostnames-file /etc/puk/region-hostnames.json --up-mbps 20 --down-mbps 40
    sudo bash deploy_tcp_brutal_region.sh --stage --region JPX --tls-sni jpx.20100823.xyz --identity-file /etc/jpx/vless-identities.json --hostnames-file /etc/puk/region-hostnames.json --up-mbps 20 --down-mbps 40
    sudo bash deploy_tcp_brutal_region.sh --deploy --region JPX --tls-sni jpx.20100823.xyz --identity-file /etc/jpx/vless-identities.json --hostnames-file /etc/puk/region-hostnames.json --up-mbps 20 --down-mbps 40

The 20/40 Mbps values are placeholders, not measured optimal rates. The
dedicated service does not modify global BBR, SSH policy, UDP 11389 or
REALITY TCP 11443, nor does it add broad brutalctl destination-prefix rules.
It adds a narrow certificate-renewal restart hook if certbot uses hooks.

## Rollback

    sudo bash activate_tcp_brutal_canary.sh --rollback
    sudo systemctl is-active jpx-proxy nginx s-ui || true

Rollback stops only the separate canary, preserving installed modules and
original proxy and management services.

## Deployment status (last confirmed 2026-10-18)

JPX: DNS-only family A/AAAA records exist. Original HY2 Direct and REALITY
Direct were externally validated BEFORE the kernel reboot. Matching headers
and DKMS build tools were installed and a kernel reboot initiated. At the
last observation CXL and MSDT could not connect to JPX over IPv4 or IPv6,
including TCP 22. Post-reboot module status and canary deployment are
therefore NOT confirmed; do not advertise any TCP Brutal JPX node.

UK and JPS: custom SSH management ports reachable but no usable privileged
authenticated session. No module deployment.

DE, US, JP, TW: privileged live connection not established; no module
deployment. Each address family must have independently proven connectivity.

Cloudflare unified PUK Worker: remains unchanged. JPX secret-protected
source onboarding and per-user aggregation remain incomplete.

Known open work: production authenticated aggregation; true WARP egress
variants; capacity control; restart/recovery monitoring; real native v2
socket group behavior; Android and Windows acceptance. GitHub Issues is
disabled on this repository (create returned 410); issues are tracked
within PR #4 and this ledger.
