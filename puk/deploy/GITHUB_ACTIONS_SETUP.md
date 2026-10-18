# Region-scoped GitHub Actions deployment

Use the manual GitHub Actions workflow named PUK TCP Brutal controlled rollout
when CXL/MSDT connectivity is poor. This workflow is intentionally manual,
environment-gated and disabled until real per-region credentials have been
configured. It does not run automatically on pushes or pull requests.

The workflow appears in the Actions UI after its file reaches the repository's
default branch. Until then it remains a staged PR change.

For each region (jpx, uk, de, us, jp, jps, tw) set up a separate GitHub
Environment named puk-REGION. Restrict deployment branches and use required
reviewers for production. Set the following Environment **variables**:

- PUK_SSH_HOST: the actual approved management DNS hostname for that region
- PUK_SSH_PORT: existing SSH management port (not always 22)
- PUK_TLS_SNI: hostname covered by that VPS's public TLS certificate
- PUK_IDENTITY_FILE: absolute private on-host JSON path with the existing
  subscriber UUID mapping; do not copy or create active user secrets in Actions
- PUK_FAMILY_FILE: absolute private on-host family-hostnames JSON path; only
  include families with working DNS and usable address routes
- PUK_UP_MBPS and PUK_DOWN_MBPS: conservative provisional integer rate limits,
  adjusted later from real measurements

Set exactly these Environment **secrets**:

- PUK_SSH_PRIVATE_KEY: private key provisioned for the approved deployment
  identity on this VPS. Do not put it in repository files or logs.
- PUK_SSH_KNOWN_HOSTS: independently verified known_hosts entry for that VPS.
  For nonstandard ports the hostname takes bracketed host-and-port form.
  Do not blindly trust ssh-keyscan without comparing its fingerprint with
  the provider console or another verified out-of-band source.

The current workflow requires authorized root SSH key authentication; it never
accepts a login password and cannot generate SSH permissions. A missing
secret/variable fails before initiating a remote connection.

Select the region and stage (default) to install the pinned scripts and render
private candidate configs without starting a new listener. Select deploy to
explicitly install the module and activate the separate TCP 14443 canary.
Each run uploads to a commit-specific directory on the VPS. Its per-region
concurrency lock prevents overlap among GitHub Actions jobs, but check
other local agents and services before invoking it.

The destination already needs Python3, sing-box, an existing trusted TLS
certificate, private user identities, private family mapping and suitable
Debian kernel headers. On a host with unmatched running headers, the script
fails and does not reboot or replace existing proxy services.

The workflow does not publish Cloudflare Worker subscription links. Stage
per-user sing-box JSON profiles are root-only until a secure and authenticated
subscription source is configured. Do not make the root-only bundle publicly
readable to work around missing Worker credentials.

SSH to JPX was unreachable over its known IPv4 and IPv6 addresses during the
2026-10-18 check. GitHub Runner egress cannot repair a VPS that is globally
offline or has an unsuccessful reboot. Restore provider-console access first
when necessary. No remote deployment has yet been demonstrated.
