# PUK Bond wire protocol v1 (experimental)

A carrier is an ordered reliable byte stream. TCP/TLS and binary WebSocket adapters are provided.
Public endpoints require TLS. An unencrypted endpoint is only accepted at a loopback destination reached
through an explicit proxy path. HMAC authenticates the hub/session but does not replace carrier encryption.

Each leg first sends a 4-byte unsigned big-endian length, followed by UTF-8 JSON (at most 8192 bytes):
`version`, `server`, `user`, `session`, `role`, `target`, `at`, `mac`.
Version is 1; session is 32 random bytes in hex; role is `up` or `down`; target is host:port;
`at` is a Unix second within 60 seconds of server time. A user's key is 32 random bytes.

MAC input is the Go `encoding/json` encoding of the hello struct in the field order above,
with mac set to the empty string. MAC is lowercase hexadecimal HMAC-SHA256. The struct fields
are exactly those defined in protocol.go; this JSON canonicalization is part of this experimental version.
The server's acknowledgment is a length-prefixed ASCII hexadecimal HMAC-SHA256 of
`hello.mac + "/accepted/" + serverID`. Clients verify it before sending website payload.
Both legs must have the same user, server, session, target and timestamp, and opposite roles.
A consumed session cannot be reattached or resumed. Incomplete pairing expires after 15 seconds by default.

After both legs attach, server dials target exactly once and sends a downlink READY frame.
Frames use a 4-byte unsigned big-endian length including a 1-byte type and payload, capped at 65536 bytes.
Types: DATA=1 (up/down), FIN=2 (up/down, no payload), READY=4 (down), ERROR=5 (down, generic error).
TCP ordering and backpressure are retained. FIN only half-closes the payload direction.
Carrier closure/error terminates the session. The implementation makes no automatic retry of website writes.

Authentication limits, idle timeout, binary WS message limits and frame limits bound resource use;
the protocol has not undergone an independent security audit. This specification is not a claim of
compatibility with stock sing-box or XHTTP clients.
