# Native Bond verification

2026-10-17:

- `puk/bond` race tests: TLS direct download and WebSocket upload via two real SOCKS bridges,
  8 MiB upload/download hash integrity, one common target connection, TCP half-close.
- 16 concurrent isolated sessions; invalid key and hub rejection; duplicate role and replay rejection;
  incomplete pairing timeout; failed upload tears down the website connection.
- Profile library compilation preserves imported detour chains and rejects chain cycles / mismatched hubs.
- Local editor rejects missing authorization and foreign Origin. Offline Go/WASM compiler produces native
  client configuration and server template in Node.
- Native sing-box outbound constructor/profile selection tested independently of platform network monitoring.
- Full sing-box selector + two SOCKS hop + WS/TLS integration test is provided. The local Work sandbox
  denies netlink monitoring, so that specific full-platform test skips on that restriction; CI runs it on
  ordinary Linux runners. GitHub Actions run 39019590325 passed the full-platform integration case.
- Public-module downstream consumer build verified without inheriting the repository-local replace.
- Existing adaptive engine cancellation regression repeated 100 times with race detection after its fix.
- Native Linux core and tool build; generated example accepted by native `sing-box check`.

Windows cross-build, Android APK build and normal-runner integration results are recorded by the linked
commit's GitHub Actions. A successful APK build does not establish phone VPN or socket-protection behavior.
Android device connectivity, Windows 10 physical hardware, WAN throughput, stability over a changing network,
UDP datagrams and transparent resumption are not verified here. Earlier XHTTP/TW measurements apply to the
separate compatibility implementation and are not results for this new native protocol.

Additional native verification (2026-10-17):

- Android configuration now has a TUN ingress and, when no DNS is imported, TCP DoH through Bond
  with an independent local bootstrap resolver. SDK race tests and native option decoding pass;
  physical Android VPN behavior remains unverified.
- A real Windows SDK SOCKS bridge used a Cloudflare WebSocket upload carrier and a separate
  Hysteria2 download carrier to the same hub. Both directions transferred 8 MiB with matching SHA-256;
  eight parallel HTTPS requests returned 200; breaking either carrier failed the connection.
  This verifies the session/carrier combination, not Android or the entire desktop GUI workflow.
- The successful download took 11.45 seconds. An earlier attempt timed out at 30 seconds with a partial
  body; a later test used a 60-second ceiling. Changing network conditions mean these are not controlled
  throughput comparisons or evidence of a causal BBR speedup.
