# JPX edge: deployment, validation and rollback

This deployment renderer emits a **candidate** sing-box configuration plus eight per-user base64 subscription sources. Every actual connection key, subscriber UUID, REALITY private key and source token exists on the VPS only, never in GitHub.

## Rollout

1. Confirm independent SSH, direct DNS for jpx.20100823.xyz, and public-CA certificate files at /etc/sing-box/tls (group sing-box).
2. Execute python3 render_jpx.py as root. It preserves existing on-host HY2 credentials and REALITY keypair and writes config.json.next; it does not publish anything.
3. Run sing-box check -c /etc/jpx/proxy.json.next. Back up current configuration, set group sing-box, atomically promote the candidate, then restart the isolated jpx-proxy systemd service.
4. Confirm UDP 11389 (HY2) and TCP 11443 (VLESS REALITY). From an external Windows/Android client, proxy Google generate_204 and normal HTTPS with trusted TLS verification.
5. Expose /srv/jpx-subs/ssN only behind an HTTPS origin protected by a long random token. Inject the token into the unified Cloudflare Worker using a **secret binding**, not script text. Never publish these guessable origin paths unauthenticated.
6. Add JPX to the PUK Worker regional fetch list and its readable-name parser; verify SS1–SS8 base64, sing-box JSON and Clash exports. Retain a verified rollback deployment.
7. Do not publish WARP, Cloudflared, IPv6 or dual variants until their exact transport and egress routes have independently passed external tests.

## Non-negotiable invariants

- HY2 SNI equals jpx.20100823.xyz and uses a valid trusted origin certificate. Never set allowInsecure or skip-cert-verify.
- REALITY uses its own keypair/short ID.
- Management ports 2095/2096 remain loopback-only and protected by a persistent firewall. This service is isolated from S-UI, which originally had zero managed inbounds/clients; do not pretend otherwise.
- All source authentication keys, identities and subscription bodies are private.
- Keep an operational SSH route, backup config and automated rollback. ACME renewal must also reload/restart the actual sing-box TLS consumer.
- Adaptive score graph, Brutal tuning, split upload/download path, and WARP policy egress are separate subsequent phases.

## Evidence, 2026-10-17

A Windows/CXL host externally connected to JPX HY2 and REALITY independently. Each protocol returned Google HTTPS 204 and Cloudflare HTTPS 200; TLS verification was successful. The JPX systemd proxy listened on UDP 11389 and TCP 11443. These are application smoke tests, not bandwidth, all-client or IPv6 certification.

## Issue ledger

GitHub Issues was disabled on welkin-moon/sing-box at the time of deployment (issue create returned HTTP 410). Track these until the repository setting is changed:

- Finish authenticated S-UI migration or formally retain isolated service management.
- Validate and repair TW/JPS origin HY2 certificates before removing production insecure=true flags. Never mass-toggle verification without first fixing the origin.
- Enforce secure central subscription access if the current /sub/ssN URL is guessable; plan migration.
- Test real Google-WARP / All-WARP egress and independent IPv6 before enabling corresponding matrix entries.
- Add application-aware end-to-end tests and a quality-scoring control plane; no false passing URL tests.

## IPv6 listener and per-family DNS checkpoint

2026-10-17 external testing from MSDT established that JPX's REALITY TCP 11443 was reachable over its IPv6 address, but the HTTPS health endpoint on IPv6 TCP 443 timed out. Inspection of the origin nginx vhost showed only an IPv4 443 listener. The Cloudflare zone now has separate DNS-only records: jpx4 (A only), jpx6 (AAAA only), jpxdual (both A and AAAA); none should be advertised as a tested TCP Brutal route yet.

The included enable_jpx_nginx_ipv6.py adds a bracketed IPv6 443 SSL listener while preserving the existing hostname, TLS certificate and protected subscription source. It retains a backup, validates nginx -t before reload and restores the previous configuration on errors. Run with --check-only first. Five offline tests passed on Windows/CXL Python 3.12. Applying this fix to the origin and running external IPv6 HTTPS verification are still pending because post-reboot SSH authentication could not be completed. An open IPv6 TCP port is not the same as successful proxy authentication and application routing.
