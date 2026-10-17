# TCP Brutal v2: verified-only PUK matrix

## Purpose and constraints

TCP Brutal v2 is a Linux TCP congestion-control kernel module, not a new wire protocol. It can improve verified VLESS/TCP connections when UDP/HY2 is blocked or TCP is lossy. It changes only the sender: clients on Windows and Android need no kernel module for server downlink acceleration.

**Do not set Brutal as the Linux global default congestion control.** A socket lacking configured rates can run at only 1 Mbps. Retain BBR and the existing HY2 / REALITY+XTLS-Vision as fallback.

## Two deployment techniques

1. v2 destination rules: On the sending VPS, use brutalctl add CLIENT_IPV4/32 RATE_MBPS or a *verified single* IPv6 /128. View via brutalctl list and /proc/net/tcp_brutal/rules. Re-adding an exact address updates existing group rates immediately; brutalctl del removes new-connection coverage. These rules do not persist across reboot. **Danger**: they cover all TCP sessions toward that host, including SSH. Never use a wide prefix, shared NAT administrator address or an unspecified client address. No production broad destination rules.
2. Socket-level group IDs (preferred): enable TCP_CONGESTION=brutal and set TCP_BRUTAL_PARAMS=23301 (rate in bytes/s, cwnd_gain tenths and a nonzero per-user group_id). This requires application support and avoids unsafe global destination rules. Upstream sing-box Multiplex Brutal also needs compatible inbound/outbound settings and is not proof that a v2 group ID is in use. Keep it on a **separate inbound**, because XTLS-Vision flow is not safe to combine blindly with Multiplex.

The official [HyNetworks/tcp-brutal](https://github.com/HyNetworks/tcp-brutal) v2 installation requires Linux 5.10+, matching kernel headers and DKMS. Inspect installation scripts and test modules rather than treating an open TCP port as evidence of Brutal.

## Family x egress rules

A region can have up to nine distinct names only after each is individually proven:

- REGION-TCPBrutal-v4-direct / -v6-direct / -dual-direct
- The same for google (Google-WARP) and all (All-WARP) only where those real egress paths exist and pass tests

For every advertised family, verify authenticated proxy HTTPS application tests from an external client. Dual requires functional A+AAAA records and tested fallback; an IPv6 address in Linux by itself does not count. An egress labeled WARP must not in fact be Direct.

The private capability manifest fed to tcp_brutal_matrix.py must confirm: running v2 module, active server downlink Brutal (not BBR), authenticated TCP proxy test, measured safe download rate, family-specific external TCP and HTTPS tests, dual DNS and external test, and real egress policy checks. The tool outputs **planning metadata**, not working proxy credentials or subscription URLs.

## Rate control

A conservative initial rate must come from measurement, not a guessed VPS uplink capacity. Tune by goodput, p95 RTT, retransmits, CPU and traffic overhead. Do not lower a rate solely because the user is idle. Apply small upward experiments and quick congestion backoff; timebox experiments and roll back to known stable BBR after failures. To avoid affecting SSH, prefer per-user socket group IDs to destination-prefix rules.

## Deployment/rollback checklist

1. Confirm SSH/console recovery, read-only inventory of running kernel, headers, provider IPv4/IPv6 and existing services.
2. Install and load TCP Brutal v2 module only on supported Linux hosts. Verify actual v2 and that each dedicated TCP listener uses it under a sending workload.
3. Preserve HY2 and REALITY+Vision services. Publish a separate TCP+Brutal listener only after a real end-to-end test.
4. Validate IPv4/IPv6/Dual separately from Windows or Android: Google 204, full HTTPS apps, RTT/loss/goodput and TLS authentication.
5. Verify Direct, Google-WARP and All-WARP *as different egress routes*. Do not invent country/family/policy entries.
6. Stage a rollback-safe subscription update across base64/sing-box JSON/Clash, preserving previous nodes. Never put user passwords, keys or working subscription URLs into public git.
7. Add a maintenance check for kernel upgrades, DKMS rebuilds and a BBR fallback; never restart the full TUN for a local rate change.

## Current verified facts as of 2026-10-17

JPX was found booted on kernel 6.12.57+deb13-cloud-amd64; Debian 13 already had a 6.12.113+deb13-cloud-amd64 kernel installed. Matching headers, DKMS and build tools, plus a 1 GiB swap file, were installed before initiating the next kernel boot. SSH port 22 came back, but authentication to the new session is blocked by tool safety checks, so TCP Brutal module installation, post-reboot kernel version and group tests are **not yet verified**.

JPX already ran independently verified HY2 Direct (UDP 11389) and VLESS+REALITY Direct (TCP 11443): external Windows/CXL clients got HTTPS Google 204 and Cloudflare trace 200 over both. These are **not** TCP Brutal verification.

Other region servers did not accept existing SSH keys. Existing production subscriptions were not modified to add untested Brutal entries.

## Open work

- Authenticated per-user Brutal group_id and dynamic server/client bandwidth negotiation.
- JPX and other nodes: module install, packet pacing proof, IPv4/IPv6 and per-policy verification.
- Integrate verified entries into existing Cloudflare Worker aggregation with rollback.
- Remove insecure TLS settings from TW/JPS HY2 *after* origin certificates are fixed.
- Complete JPX origin subscription authentication and S-UI management integration.

GitHub Issues is disabled on this repository (API HTTP 410); these items remain tracked here and in PR until Issues becomes available.
