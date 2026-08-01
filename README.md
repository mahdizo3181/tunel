# tunel — WSS/CDN-fronted reverse tunnel with anti-DPI padding

Two binaries:

- **gate** (`server/`) — runs in the restricted region. Listens on 443
  behind a CDN. Never needs an inbound port opened *for the tunnel itself*;
  the tunnel connection is dialed in by the bridge. Accepts local user
  connections on `forward_listen_addr` and forwards them through the tunnel.
- **bridge** (`client/`) — runs in the unrestricted region. Actively dials
  out to the gate's CDN-fronted domain and forwards accepted streams to a
  local service (e.g. a VPN panel) on `target_addr`.

```
end users → [gate:forward_listen_addr] → yamux stream ┐
                                                        │ single WSS conn, 443,
gate:443 ←───────────── CDN (Cloudflare) ──────────────┘ padded, uTLS ClientHello
   ↑ dialed by bridge
bridge → [target_addr, e.g. 127.0.0.1:1194 VPN panel]
```

## Quick install (VPS one-liner)

`install.sh` is a self-contained installer + management dashboard: it
installs Docker/git if missing, clones this repo, and walks you through
setting up whichever side belongs on that machine.

```
bash <(curl -fsSL https://raw.githubusercontent.com/<you>/<repo>/main/install.sh)
```

Run it as root on the restricted-region box and choose **[1] Setup & Start
Server (Gate)**; run it again on the unrestricted-region box and choose
**[2] Setup & Start Client (Bridge)**. It also has menu options to view live
logs, restart, and stop. Re-running `bash <(curl ...)` (or `./install.sh`
from an existing checkout) later pulls the latest code and reopens the same
menu — see the script's header comment for the `TUNEL_REPO_URL` env var to
point it at your own fork.

Session direction is reversed from data direction: the bridge dials the
gate (so the gate needs no open inbound port of its own), but the gate is
the side that opens yamux streams — one per accepted local user connection
— and the bridge accepts and forwards them.

## Anti-DPI design

- **Single multiplexed connection.** All user TCP sessions ride one
  `hashicorp/yamux` session over one TCP connection — no per-user
  connection-rate signal for the firewall to key off of.
- **WSS, CDN-compatible.** The mux is carried entirely inside a standard
  WebSocket-over-TLS upgrade, so it passes through Cloudflare (or any
  WS-aware CDN) like ordinary browser traffic.
- **Port 443 only.** The gate never listens anywhere else.
- **uTLS ClientHello.** The bridge dials out using
  `refraction-networking/utls` with the `HelloChrome_Auto` fingerprint, so
  the JA3/JA4 fingerprint matches a real Chrome browser instead of Go's
  `crypto/tls` default (an easy DPI tell).
- **Random frame padding + chunking.** Every WebSocket message is
  `[2-byte length][payload][random pad bytes]`, and large writes are split
  into randomly-sized chunks before framing (`common/padded_conn.go`). No
  two frames on the wire share a stable length signature.
- **Decoy fallback.** Any request that isn't a WS upgrade with the correct
  path *and* the correct secret header is served a boring static HTML page
  (`server/decoy.html`) with a 200, so probing the origin directly looks
  like hitting an idle web server.
- **Aggressive keepalive + self-healing.** WSS-level ping/pong
  (`ping_interval_seconds` / `pong_timeout_seconds`) plus a short yamux
  keepalive interval detect a dead link quickly; the bridge then reconnects
  with exponential backoff + jitter, resetting the backoff once a session
  has stayed up longer than `stable_conn_millis`.
- **Tuned for throughput.** `SO_RCVBUF`/`SO_SNDBUF` are set on every socket
  (`tcp_buffer_bytes`), `TCP_NODELAY` is enabled, and the copy loops use a
  `sync.Pool` of 32KiB buffers instead of allocating per connection.

## Layout

```
common/   shared: padding/framing, yamux config, uTLS dialer, buffer pool, pipe
server/   the gate binary (main.go, config.go, decoy.go, decoy.html)
client/   the bridge binary (main.go, config.go)
config.server.example.json
config.client.example.json
Dockerfile.server
Dockerfile.client
docker-compose.yml
.env.example
```

## Configuration

Both sides read a JSON config (`-config path.json`), with the auth token
overridable via the `TUNNEL_AUTH_TOKEN` environment variable (preferred, so
the secret doesn't have to sit in a file on disk).

**`config.server.example.json`** (gate):

| field | meaning |
|---|---|
| `listen_addr` | where the gate listens for the WSS tunnel; keep it `:443` |
| `tls_cert_file` / `tls_key_file` | origin cert; leave both empty only if the CDN terminates TLS in Flexible mode (not recommended, see below) |
| `ws_path` | HTTP path that identifies a real handshake attempt |
| `auth_header` / `auth_token` | header name + secret the bridge must present |
| `forward_listen_addr` | local port end users connect to |
| `decoy_html_path` | optional custom decoy page (defaults to a generic bundled page) |
| `padding` | `min_pad_bytes` / `max_pad_bytes` / `min_chunk` / `max_chunk` |
| `ping_interval_seconds` / `pong_timeout_seconds` | WSS keepalive |
| `tcp_buffer_bytes` | SO_RCVBUF/SO_SNDBUF size |

**`config.client.example.json`** (bridge):

| field | meaning |
|---|---|
| `server_url` | `wss://your-domain/ws-path` — the CDN-fronted domain, **not** the gate's real IP |
| `sni` | TLS SNI override (defaults to the host in `server_url`) |
| `target_addr` | local service to forward to, e.g. `127.0.0.1:1194` |
| `insecure_skip_verify` | only for local testing against a self-signed cert |
| `reconnect_min_backoff_millis` / `reconnect_max_backoff_millis` | backoff bounds |
| `stable_conn_millis` | a session up this long resets backoff to the minimum on next drop |

`auth_header`, `auth_token`, and `padding` must match on both sides.

## Running locally (no Docker)

```
go build -o gate ./server
go build -o bridge ./client

cp config.server.example.json config.server.json   # edit: certs, token, ports
cp config.client.example.json config.client.json   # edit: server_url, token, target

TUNNEL_AUTH_TOKEN=... ./gate   -config config.server.json
TUNNEL_AUTH_TOKEN=... ./bridge -config config.client.json
```

## Docker

```
cp .env.example .env   # set TUNNEL_AUTH_TOKEN
# on the restricted-region host:
docker compose up -d --build gate
# on the unrestricted-region host:
docker compose up -d --build bridge
```

Both services use `network_mode: host` (Linux only) — the gate needs to
bind 443 directly with nothing DPI could fingerprint as an extra NAT hop,
and the bridge needs to reach `target_addr` on the *host's* loopback (e.g.
a VPN panel running directly on that machine, not in a container). If your
panel runs in its own container instead, drop `network_mode: host` from the
`bridge` service, put it on the panel's Docker network, and set
`target_addr` to the panel container's name:port.

The images build with vendored dependencies (`vendor/`, committed), so
`docker build` doesn't need module-proxy access at build time — useful
since the gate side is, by definition, on a restricted network.

## Cloudflare configuration

1. **DNS**: create an `A`/`AAAA` record for the domain the bridge will dial
   (e.g. `tunnel.example.com`) pointing at the gate's real IP, with the
   proxy status **On** (orange cloud). This is what actually hides the
   gate's IP from the restricted-region firewall — only Cloudflare's edge
   IPs are ever visible to it.
2. **SSL/TLS mode → Full (strict)**. Issue a Cloudflare **Origin CA**
   certificate (SSL/TLS → Origin Server → Create Certificate) and use it as
   `tls_cert_file`/`tls_key_file` on the gate. Avoid Flexible mode — it
   means Cloudflare↔origin traffic is plaintext HTTP, which both leaks the
   protocol to anything between Cloudflare and your origin and defeats the
   point of a TLS-shaped tunnel.
3. **WebSockets**: on by default for all plans; nothing to toggle. Just
   confirm Network → WebSockets is enabled.
4. **Timeouts**: Cloudflare free/pro proxied connections are idle-killed
   around 100s. Keep `ping_interval_seconds` well under that (default 10s)
   so the connection never looks idle.
5. Leave everything else (caching, minification, Rocket Loader, etc.) at
   defaults — they only apply to normal HTTP responses and don't touch the
   proxied WS traffic; the decoy page is plain static HTML either way.
6. Point `server_url` in the bridge's config at the fronted domain
   (`wss://tunnel.example.com/<ws_path>`), never at the gate's real IP —
   dialing the IP directly bypasses the CDN and defeats the whole point.

## Notes on the yamux role reversal

The gate calls `yamux.Client(conn, ...)` and the bridge calls
`yamux.Server(conn, ...)`, even though at the TCP/WS layer the bridge is
the one that dials out. yamux's client/server role only decides which side
owns odd vs. even stream IDs when opening streams — it's independent of
which side established the underlying connection. The gate needs to be the
stream-opener (one new stream per local user connection), so it takes the
yamux "client" role.
