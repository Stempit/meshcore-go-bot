# Channel message delivery reliability

How the bot makes channel (group) replies actually reach the mesh, given that
MeshCore channel messages have **no delivery acknowledgement**.

All the logic lives in [`internal/meshcore_bt/client.go`](../internal/meshcore_bt/client.go);
the firmware it reasons about is vendored at [`MeshCore/`](../MeshCore/).

---

## The problem

Channel replies were logged as sent, but never appeared on the network:

```
msg="Channel message queued by device" channel=#ping-pong
msg="Channel reply sent" channel=#ping-pong
# …and yet no other node ever saw it
```

## Root cause: `respCodeOk` means *queued*, not *transmitted*

When the bot sends `CMD_SEND_CHANNEL_TXT_MSG`, the firmware's
`BaseChatMesh::sendGroupMessage` builds and **queues** the packet, then
immediately writes the `OK` frame
([`MyMesh.cpp` CMD_SEND_CHANNEL_TXT_MSG](../MeshCore/examples/companion_radio/MyMesh.cpp)).
`OK` is sent the moment the packet enters the outbound queue — **not** when it
goes out over RF.

After `OK`, the `Dispatcher` can still **silently drop** the queued packet with
no notification back to the companion app:

| Cause | Firmware location |
|-------|-------------------|
| TX duty-cycle / airtime budget exhausted | `Dispatcher::checkSend` `tx_budget_ms` |
| Channel-busy (CAD) for too long | `Dispatcher::checkSend` CAD path |
| `startSendRaw` failure → `logTxFail` | `Dispatcher::checkSend` |
| Outbound send queue full → dropped | `StaticPoolPacketManager::queueOutbound` |

Channel messages have no ACK, and the firmware's **seen-hash dedup** suppresses
RF rebroadcasts of our own packet from reaching the companion as a decoded
message. So the bot has no built-in way to learn the packet was lost.

**Why a retry loop did not help:** `OK` arrives reliably on the first attempt,
so any "retry on rejection" logic never fires while the packet is lost on air.

---

## Fix part 1 — transmit the same packet several times

Mirroring the official apps, the bot transmits the **same packet N times**.
The trick that makes this invisible to users:

- The firmware dedup hash is `SHA256(payloadType ‖ payload)[:8]`
  (`Packet::calculatePacketHash`, `MAX_HASH_SIZE = 8`).
- The group payload begins with the 4-byte message timestamp, and group
  encryption is **deterministic** (AES-128-ECB + HMAC-SHA256, no IV —
  `Utils::encryptThenMAC`).
- So reusing **one timestamp + identical text** yields an identical
  `packet_hash`. Receivers dedup it and show the message **once**, while each
  transmission is an independent chance to win the channel.

The node's own `Mesh::sendFlood` marks-then-sends (`hasSeen` only blocks
*inbound* rebroadcasts), so our own repeated outbound copies all go out on air.

## Fix part 2 — adaptive early-stop (listen for the mesh repeating us)

Blindly sending all N copies loads the device and the channel. Instead, after
each transmission the bot **listens for the mesh rebroadcasting our exact
packet** and stops as soon as it hears it.

This is possible because the firmware emits the raw `0x88 LOG_RX_DATA` frame for
**every** received RF frame *before* the dedup check
([`Dispatcher.cpp` `checkRecv` → `logRxRaw`](../MeshCore/src/Dispatcher.cpp)).
So a repeater's rebroadcast of our own message reaches the bot as raw bytes even
though no decoded `0x11` message does.

Matching, with no decryption needed:

1. Compute the expected `packet_hash` for our outgoing message — reusing the
   already-present `buildGRPTXTPacket` crypto (originally written for MQTT TX
   publishing).
2. For every incoming `0x88` frame, extract `payloadType` + payload
   (`rawFramePayload`, mirrors `Dispatcher::tryParsePacket`) and hash it.
3. The hash excludes route bits, transport codes, and path, so our send and any
   repeater's rebroadcast (which appends path hashes) hash **identically**.
4. On a match, signal the sender to stop.

`channel_transmissions` is therefore a **maximum cap**, not a fixed count. On a
healthy mesh the bot usually stops after the first transmission. Graceful
fallback to spaced blind sends when the hash can't be computed (no PSK /
self-info) or no repeater is in range.

Log line `confirmed_by_mesh=true` is the first real positive confirmation the
bot has for a channel message — something MeshCore otherwise never provides.

## Fix part 3 — size the listen window from real LoRa airtime

A repeater's flood backoff is **random and preset-dependent**:
`Mesh::getRetransmitDelay` = `nextInt(0,5) × (airtime × 0.52)`, i.e. up to
~2.6×airtime, plus the rebroadcast's own airtime
([`Mesh.cpp`](../MeshCore/src/Mesh.cpp)). A fixed window is therefore wrong
across radio presets (a fixed 3 s once missed a 3.4 s SF11/250 repeat by 0.4 s
and sent a needless second copy).

`repeatWindowFor` / `loraAirtimeMs` compute real time-on-air from `SelfInfo`
(`Sf`, `BwHz`, `Cr`), matching RadioLib's `getTimeOnAir` (Semtech AN1200.13):

- explicit header, CRC on
- preamble 32 symbols for SF ≤ 8, else 16 (`preambleLengthForSF`)
- low-data-rate optimize when symbol time > 16 ms
- MeshCore `cr` is 5..8 (4/5..4/8), which equals the `(CR+4)` symbol multiplier

Window = `4 × airtime + 1500 ms`, clamped to **[3 s, 12 s]**.

Worst-case time to hear a repeat ≈ our TX (~1×airtime) + backoff (~2.6×airtime)
+ rebroadcast (~1×airtime); the window sits just above it.

Bandwidth note: the firmware sends BW as `uint32(_prefs.bw * 1000)` with the
multiply done in float, so fractional bandwidths survive — **62.5 kHz → 62500
Hz** exactly, no truncation.

### Example windows

| Preset | airtime (~76 B frame) | window |
|--------|----------------------|--------|
| SF7 / 500 kHz | ~40 ms | 3.0 s (min clamp) |
| SF8 / 62.5 kHz | ~0.6–0.8 s | ~3.9–4.8 s |
| SF10 / 250 kHz | ~0.4 s | ~3.3 s |
| SF11 / 250 kHz | ~0.8 s | ~4.7 s |
| SF12 / 125 kHz | ~3.5 s | 12 s (max clamp) |

Longer messages produce longer windows (more bytes → more airtime); UTF-8
Cyrillic is 2 bytes/char, so Russian replies are markedly larger on air than
they look. The opening log line prints the chosen value: `repeat_window=4.322s`.

---

## Configuration

```yaml
Connection:
  # Maximum number of times to transmit each channel reply. The bot stops early
  # once it hears the mesh rebroadcast the message, so fewer are sent on a
  # healthy network. Default 3; 1 = single transmission.
  channel_transmissions: 3
```

The listen window is derived automatically from airtime; there is no separate
window setting.

## Terminology

- **transmission / `tx`** — *our* node sending the same packet (this feature).
- **repeat / retransmit** — a *repeater node* rebroadcasting a packet across the
  network (a different, inbound concept). Deliberately not reused for our sends.

## Code map (`internal/meshcore_bt/client.go`)

| Symbol | Role |
|--------|------|
| `SendChannelMessageWithRetry` | Entry point: arm watch, transmit up to N times, stop early on mesh repeat |
| `sendChannelOnce` | One transmission + wait for device `OK` (fixed timestamp) |
| `sendChannelTextMessageTS` | BLE write with a caller-supplied timestamp |
| `buildGRPTXTPacket` | Deterministic GRP_TXT packet (AES-ECB + HMAC); also used by MQTT TX |
| `packetHash` | `SHA256(payloadType ‖ payload)[:8]` — the firmware dedup hash |
| `rawFramePayload` | Extract payloadType + payload from a raw `0x88` frame |
| `armRepeatWatch` / `disarmRepeatWatch` / `noteRepeatCandidate` | Match incoming raw frames against the expected hash; hooked into `parseLogRxData` |
| `loraAirtimeMs` / `repeatWindowFor` | Airtime estimate → listen window |

## Log reference

```
msg="Sending channel message with retry" … max_transmissions=3 adaptive=true repeat_window=4.322s
msg="Channel message transmission queued by device" channel=#ping-pong tx=1/3
msg="Channel message repeated by mesh, stopping early" channel=#ping-pong transmissions_sent=1
msg="Channel message transmitted" transmissions_sent=1 max_transmissions=3 confirmed_by_mesh=true
```

- `adaptive=true` — the expected hash was computed; early-stop is active.
- `confirmed_by_mesh=true` — a rebroadcast of our packet was actually heard.
- `transmissions_sent` < `max_transmissions` — stopped early (healthy mesh).
