# mqttbridge — MQTT publishing + Let's Mesh analyzer

Package `mqttbridge` publishes mesh packet activity to MQTT: a custom broker and/or the
Let's Mesh analyzer servers (US/EU). It manages multiple concurrent connections with
automatic reconnection, and signs analyzer connections with Ed25519 JWTs.

> Changes here affect network-visible state (live broker connections, published
> packets). `bridge.go` is ~1700 lines — read before editing.

## What it publishes

Three message types, per the MeshCore MQTT protocol:

| Type | Topic suffix | Retained | Contents |
|------|--------------|----------|----------|
| **Status** | `/status` | yes | device online/offline + metadata + stats (battery, uptime, air time, noise floor…) |
| **Packets** | `/packets` | no | full packet: direction, route, SNR/RSSI, hash, path, hex payload |
| **Raw** | `/raw` | no | minimal raw packet data for map integration |

Topics follow `meshcore/{IATA}/{DEVICE_PUBLIC_KEY}/{type}`.

## Authentication

- **Custom broker:** optional username/password; optional TLS (CA cert + client cert/key).
- **Let's Mesh analyzers:** JWT, username `v1_{UPPERCASE_PUBLIC_KEY}`, token signed with
  the device's Ed25519 private key (claims `iss`/`sub`/`iat`/`exp` (24h)/`owner`/`email`).
  CA is Google Trust Services GTS Root R4. Endpoints:
  `wss://mqtt-us-v1.letsmesh.net:443/mqtt`, `wss://mqtt-eu-v1.letsmesh.net:443/mqtt`.

## Reconnection

Exponential backoff, doubling per consecutive failure, reset on success. Custom broker
base delay 15s, analyzers 60s; both capped at 300s.

## Configuration

YAML key `MQTT`.

```yaml
MQTT:
  # custom broker (optional)
  server: "broker.example.com"
  port: 1883
  username: ""
  password: ""

  iata: "SEA"                    # airport code → topic namespace

  status_enabled: true
  packets_enabled: true
  raw_enabled: false
  tx_enabled: false              # publish TX (sent) packets too
  status_interval: 300000        # status publish interval, ms

  analyzer_us: true              # Let's Mesh US analyzer
  analyzer_eu: true              # Let's Mesh EU analyzer

  # owner identity (for analyzer JWTs)
  owner_public_key: "<64 hex>"
  owner_email: "you@example.com"
  device_private_key: "<64 hex>" # Ed25519 private key for JWT signing

  # TLS (custom broker)
  use_tls: false
  ca_cert: ""
  client_cert: ""
  client_key: ""
```

## Protocol reference

The full MeshCore MQTT protocol spec (transports, all JSON message fields, packet-type
codes, firmware CLI, subscriber examples) is retained below for reference; the Go bridge
above implements the publishing side of it.

---

### Transports

- TCP: `mqtt://host:1883` (plain), `mqtts://host:8884` (TLS).
- WebSocket (firewall-friendly): `ws://host:80/mqtt`, `wss://host:443/mqtt`. The
  analyzer servers are WebSocket-only.

### Packet message fields

`origin`, `origin_id`, `timestamp`, `type` (`PACKET`), `direction` (`rx`/`tx`), `time`,
`date`, `len`, `packet_type` (0–15), `route` (`F`/`D`/`T`/`U`), `payload_len`, `raw`
(hex), `SNR`, `RSSI`, `hash`, and (direct packets) `path`.

Packet-type codes: 0 REQ · 1 RESPONSE · 2 TXT_MSG · 3 ACK · 4 ADVERT · 5 GRP_TXT ·
6 GRP_DATA · 7 ANON_REQ · 8 PATH · 9 TRACE · 10 MULTIPART.

### Status stats fields

`battery_mv`, `uptime_secs`, `errors`, `queue_len`, `noise_floor`, `tx_air_secs`,
`rx_air_secs`, `recv_errors`.

### Client settings

Keep-alive 45s, buffer 896 B, QoS 1, auto-reconnect on. Up to 10 packets queued during
connection issues.
