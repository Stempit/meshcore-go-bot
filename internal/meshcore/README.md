# meshcore — MeshCore protocol driver

Package `meshcore` is the core dependency of the whole bot: the driver for a MeshCore
radio device over **BLE or serial**. It is a protocol state machine (`client.go`,
~2300 lines) that connects to the device, decodes incoming frames into
`Message` / `ChannelMessage` values, and sends replies with delivery reliability logic.
Everything else depends on this package.

> `client.go` is a BLE protocol state machine. The `sentConfirmCh` channel is a single
> shared slot — only one send can be in flight at a time, so concurrent senders must
> serialise (which the [dispatch](../dispatch/README.md) sender does).

## Responsibilities

- **Transport** — connect over Bluetooth LE (default) or serial/USB (`connect*.go`).
- **Framing/parsing** — decode device frames, including the raw `0x88 LOG_RX_DATA`
  frames used for the channel-reliability early-stop (`frames.go`, `parse.go`).
- **Sending** — DMs (`SendMessageWithRetry`, ack→no-ack fallback) and channel messages
  (`SendChannelMessageWithRetry`, multi-transmission with adaptive early-stop).
- **Adverts / contacts** — self-advert, contact list, the advert/contact stream the
  greeter hooks into.

## Channel message delivery reliability

MeshCore channel (group) messages have **no delivery ACK**, and the device's `OK` frame
only means the packet was **queued**, not transmitted — the firmware can still silently
drop it (duty cycle, CAD busy, queue full). To make channel replies actually reach the
mesh:

1. **Transmit the same packet up to N times.** Reusing one timestamp + identical text
   yields an identical dedup hash (`SHA256(payloadType‖payload)[:8]`), so receivers show
   it once while each transmission is an independent chance to win the channel.
2. **Adaptive early-stop.** After each send the bot listens for the mesh *rebroadcasting
   our exact packet* (matched against the expected hash from the raw `0x88` frames, no
   decryption needed) and stops as soon as it hears it. `confirmed_by_mesh=true` is the
   first real positive delivery confirmation MeshCore otherwise never provides.
3. **Airtime-sized listen window.** The window is computed from real LoRa time-on-air
   (`SelfInfo` Sf/BwHz/Cr), `4 × airtime + 1500 ms`, clamped to [3s, 12s], so it's correct
   across radio presets.

So `channel_transmissions` is a **maximum cap**, not a fixed count — on a healthy mesh
the bot usually stops after the first transmission.

Terminology: **transmission / `tx`** is *our* node re-sending the same packet (this
feature); **repeat / retransmit** is a *repeater node* rebroadcasting (a different,
inbound concept).

Key symbols: `SendChannelMessageWithRetry`, `sendChannelOnce`, `buildGRPTXTPacket`,
`packetHash`, `rawFramePayload`, `armRepeatWatch`/`noteRepeatCandidate`,
`loraAirtimeMs`/`repeatWindowFor`.

## Configuration

Reads the central `Connection` config:

```yaml
Connection:
  type: bt                    # bt (Bluetooth LE, default) | serial
  serial_port: "/dev/ttyUSB0" # when type: serial
  serial_baud: 115200
  channel_transmissions: 3    # MAX times each channel reply is transmitted; 1 = single
```

The firmware this reasons about is upstream
[MeshCore](https://github.com/meshcore-dev/MeshCore) (a local reference clone
lives outside this repo); the firmware MQTT protocol is documented in
[`internal/bridge/mqttbridge`](../bridge/mqttbridge/README.md).
