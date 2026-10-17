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
  ordinary Linux runners. Do not equate the constructor test with this full-platform integration test.
- Native Linux core and tool build; generated example accepted by native `sing-box check`.

Windows cross-build, Android APK build and normal-runner integration results are recorded by the linked
commit's GitHub Actions. A successful APK build does not establish phone VPN or socket-protection behavior.
Android device connectivity, Windows 10 physical hardware, WAN throughput, stability over a changing network,
UDP datagrams and transparent resumption are not verified here. Earlier XHTTP/TW measurements apply to the
separate compatibility implementation and are not results for this new native protocol.
