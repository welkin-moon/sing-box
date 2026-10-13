# PUK remote dashboard

`w.20100823.xyz` uses a dedicated Worker and four loopback Go agents. No Durable
Objects, R2, KV heartbeat writes, or SSH credentials are used by the dashboard.

Administrator authentication uses the existing dashboard password in a Worker
secret. Cookies are HMAC signed, Secure, HttpOnly and SameSite=Strict. Mutations
require the same Origin. Agent tokens never reach the browser. The six-attempt
login throttle is per Worker isolate, not a globally durable security boundary.

Agents sample S-UI's read-only SQLite counters every 15 seconds and the page
refreshes every 30 seconds while visible. HTTPS checks run every 90 seconds via
an owned loopback VLESS/WS client using the existing All-WARP route. Probe traffic
is charged to the enabled account used by that client. The page explicitly shows
sampling times, stale nodes, the retained inlet-statistics window, reset-cycle
and historical user counters. Address aliases share the inlet's counter; this
database cannot distinguish v4/v6/dual aliases of the same listener.

Rotation is administrator-only, asynchronous, idempotent and protected by a
cross-process flock plus a persisted 10-minute cooldown. It makes one bounded
wgcf registration attempt. No guarantee of a different Cloudflare-assigned IPv4
is made. Profiles without IPv6, unchanged IPv4, or failed Google/ChatGPT HTTPS
checks cause preservation or rollback. A separate systemd guard restores the
original connection after 180 seconds if the process dies during mutation.
Embedded UK endpoint rollback restores only the original options BLOBs, never
user counters or the whole S-UI database. Interface nodes preserve existing
policy routing/PostUp/PostDown and replace only profile keys and addresses.

Source files: `agent.go`, `probe.sh`, `rotate.sh`, `worker.mjs`, `page.mjs`.
Agent build: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o agent agent.go`.
Worker tests: `node --test worker.test.mjs`. Node-local credentials, generated
registrations, API token files, and rotation backups must not be committed.
