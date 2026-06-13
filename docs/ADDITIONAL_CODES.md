# Additional BLE Frame Codes — Implementation Plan

Source of truth: `MeshCore/examples/companion_radio/MyMesh.cpp`

The bot currently logs `"Unknown frame code"` for any frame code not in its switch. This document lists every code defined by the firmware that the bot does not yet handle, describes the payload layout (derived directly from the firmware source), and specifies what to implement.

---

## How to read this document

Each entry shows:
- **Hex / decimal** value
- **Firmware name** (`#define` in MyMesh.cpp)
- **Byte-by-byte payload layout** (copied from the firmware's serialisation code)
- **Trigger**: what causes the firmware to emit this frame
- **Implementation plan**: what files to change and what the code should do

---

## Prerequisite: extract `publishPendingRawToMQTT` helper

All push codes described here are triggered by received RF packets. The firmware emits a `0x88 PUSH_CODE_LOG_RX_DATA` frame for every RF packet and stores it in `pendingRaw`; the specific decoded push frame (0x84, 0x85, 0x89, etc.) follows immediately after. Every new parser must correlate with the oldest pending raw frame and publish it to MQTT — the same pattern already used in `parseAdvert`.

**Before implementing any new parser**, extract the repeated block from `parseAdvert` into a helper:

**`internal/meshcore_bt/client.go`** — add helper:
```go
// publishPendingRawToMQTT correlates the oldest pending 0x88 raw frame with this
// event and publishes it to MQTT. If no frame is available (expired or not yet
// arrived), falls back to the provided SNR/RSSI values and nil raw data so at
// minimum the RF event is recorded with signal quality.
func (c *MeshCoreClient) publishPendingRawToMQTT(fallbackSNR, fallbackRSSI float64) {
    c.mu.RLock()
    mqttBridge := c.mqtt
    c.mu.RUnlock()
    if mqttBridge == nil {
        return
    }
    rawData, snr, rssi := []byte(nil), fallbackSNR, fallbackRSSI
    for i, p := range c.pendingRaw {
        if time.Since(p.arrivedAt) <= pendingRawTTL {
            rawData, snr, rssi = p.rawData, p.snr, p.rssi
            c.pendingRaw = append(c.pendingRaw[:i], c.pendingRaw[i+1:]...)
            break
        }
    }
    mqttBridge.QueuePacket(rawData, false, snr, rssi)
}
```

Then replace the inline block in `parseAdvert` with `c.publishPendingRawToMQTT(0, 0)`.

---

## Priority 1 — Already appearing in production logs

### 0x89 — `PUSH_CODE_TRACE_DATA`

**Trigger:** The radio device received a TRACE echo packet (either: another node traced this device, or the device's own outgoing trace got echoed back through the mesh with path data). The firmware assembles the path and pushes it to the companion app unconditionally.

**Payload** (from `MyMesh.cpp:onTraceRecv`):
```
[0x89]
[0x00]          reserved
[path_len:u8]   number of hops
[flags:u8]      bits 0-1 = path_sz (SNR array stride exponent)
[tag:u32 LE]    matches the tag from the outgoing trace command
[auth_code:u32 LE]
[path_hashes: path_len bytes]          1 byte per hop = node ID hash
[path_snrs:   path_len >> path_sz bytes]  SNR per hop (int8, value*4 = 0.25 dB steps)
[final_snr:   int8]  SNR on the last leg to this node (getSNR()*4)
```

`path_sz = flags & 0x03`. When `path_sz == 0`, there is one SNR byte per hop. When `path_sz == 1`, there is one SNR byte per two hops (packed), etc. In practice firmware v1.11+ always uses `path_sz == 0` (one byte/hop); treat other values as "unknown packing, log and skip".

**Implementation — 4 files:**

**`internal/meshcore_bt/proto_consts.go`** — add constant:
```go
pushCodeTraceData = 0x89 // trace echo received; path + per-hop SNR
```

**`internal/meshcore_bt/proto_models.go`** — add struct:
```go
type TraceData struct {
    Tag        uint32
    AuthCode   uint32
    PathHashes []byte    // one byte per hop, node ID hash
    PathSNRs   []float32 // dB, one entry per hop (may be shorter if path_sz > 0)
    FinalSNR   float32   // SNR on the last leg to this node
}
```

**`internal/meshcore_bt/client.go`** — add field, parser, case, and setter:

```go
// field in MeshCoreClient struct:
onTraceData func(*TraceData)

// parser:
func (c *MeshCoreClient) parseTraceData(data []byte) {
    const minLen = 1 + 1 + 1 + 1 + 4 + 4 // code+reserved+path_len+flags+tag+auth
    if len(data) < minLen {
        c.logger.Warn("parseTraceData: too short", "len", len(data))
        return
    }
    pathLen := int(data[2])
    flags   := data[3]
    pathSz  := int(flags & 0x03)
    tag     := binary.LittleEndian.Uint32(data[4:8])
    auth    := binary.LittleEndian.Uint32(data[8:12])

    snrCount := pathLen >> pathSz
    needed   := 12 + pathLen + snrCount + 1
    if len(data) < needed {
        c.logger.Warn("parseTraceData: truncated", "len", len(data), "needed", needed)
        return
    }

    hashes := make([]byte, pathLen)
    copy(hashes, data[12:12+pathLen])

    snrs := make([]float32, snrCount)
    for i := range snrs {
        snrs[i] = float32(int8(data[12+pathLen+i])) / 4.0
    }
    finalSNR := float32(int8(data[12+pathLen+snrCount])) / 4.0

    td := &TraceData{
        Tag:        tag,
        AuthCode:   auth,
        PathHashes: hashes,
        PathSNRs:   snrs,
        FinalSNR:   finalSNR,
    }
    c.logger.Debug("Trace data received",
        "tag", fmt.Sprintf("%08x", tag),
        "hops", pathLen,
        "final_snr", finalSNR)

    // MQTT: correlate with the preceding 0x88 raw frame; FinalSNR is the fallback.
    c.publishPendingRawToMQTT(float64(finalSNR), 0)

    if c.onTraceData != nil {
        c.onTraceData(td)
    }
}

// in handleFrame switch:
case pushCodeTraceData:
    c.parseTraceData(data)

// setter:
func (c *MeshCoreClient) OnTraceData(f func(*TraceData)) {
    c.onTraceData = f
}
```

**`cmd/bot/bot.go`** — wire callback (optional for now, but stop the WARN):
The callback can log the received trace for observability.

---

## Priority 2 — New frame types worth handling

### 0x1B (27) — `RESP_CODE_CHANNEL_DATA_RECV`

**Trigger:** A channel binary data packet arrived (non-text channel message, e.g. telemetry payload). The firmware queues it like a text message and emits `PUSH_CODE_MSG_WAITING` (0x83) as a "tickle"; the actual frame is delivered when the bot calls `CMD_SYNC_NEXT_MESSAGE`.

**Payload** (from `MyMesh.cpp:onChannelDataRecv`):
```
[0x1B]
[snr*4:  int8]   SNR on receive leg
[0x00]           reserved1
[0x00]           reserved2
[channel_idx: u8]
[path_len: u8]   0xFF means flood-routed (path unknown)
[data_type_lo: u8]
[data_type_hi: u8]   data_type = uint16 LE
[data_len: u8]
[data: data_len bytes]
```

**Implementation:**

**`proto_consts.go`** — add:
```go
respCodeChannelDataRecv = 0x1B
```

**`proto_models.go`** — add struct:
```go
type ChannelDataMessage struct {
    ChannelIndex int
    DataType     uint16
    Data         []byte
    SNR          float32
    PathLength   int // 0xFF = flood
}
```

**`client.go`** — add parser, case, field, setter:

```go
onChannelData func(*ChannelDataMessage)

func (c *MeshCoreClient) parseChannelData(data []byte) {
    const minLen = 1 + 1 + 1 + 1 + 1 + 1 + 1 + 1 + 1 // 9 bytes minimum (0 payload)
    if len(data) < minLen {
        c.logger.Warn("parseChannelData: too short", "len", len(data))
        return
    }
    snr        := float32(int8(data[1])) / 4.0
    chanIdx    := int(data[4])
    pathLen    := int(data[5])
    dataType   := uint16(data[6]) | uint16(data[7])<<8
    dataLen    := int(data[8])
    if len(data) < 9+dataLen {
        c.logger.Warn("parseChannelData: truncated", "len", len(data), "data_len", dataLen)
        return
    }
    payload := make([]byte, dataLen)
    copy(payload, data[9:9+dataLen])

    msg := &ChannelDataMessage{
        ChannelIndex: chanIdx,
        DataType:     dataType,
        Data:         payload,
        SNR:          snr,
        PathLength:   pathLen,
    }
    c.logger.Debug("Channel data received",
        "channel", chanIdx,
        "data_type", fmt.Sprintf("0x%04x", dataType),
        "data_len", dataLen)
    if c.onChannelData != nil {
        c.onChannelData(msg)
    }

    // MQTT: the 0x88 raw frame may have been flushed already (delivery is delayed
    // through the MSG_WAITING + sync queue); SNR from the frame is the fallback.
    // RSSI is not present in this frame.
    c.publishPendingRawToMQTT(float64(snr), 0)

    _ = c.sendSyncNextMessage() // mirror behaviour of text channel message handlers
}

// case in handleFrame:
case respCodeChannelDataRecv:
    c.parseChannelData(data)
    _ = c.sendSyncNextMessage()

func (c *MeshCoreClient) OnChannelData(f func(*ChannelDataMessage)) {
    c.onChannelData = f
}
```

---

### 0x8E — `PUSH_CODE_CONTROL_DATA` (firmware v8+)

**Trigger:** Another node sent a raw binary control/command packet to this node's address over the mesh. Useful for building a mesh-command interface (e.g. a Telegram-side operator triggers an action by sending a control packet, the bot receives it here).

**Payload** (from `MyMesh.cpp:onControlDataRecv`):
```
[0x8E]
[snr*4:    int8]  SNR on last leg
[rssi:     int8]  RSSI on last leg
[path_len: u8]    hop count
[payload: remaining bytes]
```

**Implementation:**

**`proto_consts.go`** — add:
```go
pushCodeControlData = 0x8E // v8+: raw binary control payload from another node
```

**`proto_models.go`** — add:
```go
type ControlData struct {
    SNR      float32
    RSSI     int8
    PathLen  int
    Payload  []byte
}
```

**`client.go`** — add parser, case, field, setter:

```go
onControlData func(*ControlData)

func (c *MeshCoreClient) parseControlData(data []byte) {
    if len(data) < 4 {
        c.logger.Warn("parseControlData: too short", "len", len(data))
        return
    }
    snr     := float32(int8(data[1])) / 4.0
    rssi    := int8(data[2])
    pathLen := int(data[3])
    payload := make([]byte, len(data)-4)
    copy(payload, data[4:])

    cd := &ControlData{SNR: snr, RSSI: rssi, PathLen: pathLen, Payload: payload}
    c.logger.Info("Control data received",
        "snr", snr, "rssi", rssi, "path_len", pathLen, "payload_len", len(payload))

    // MQTT: correlate with preceding 0x88 frame; use frame SNR/RSSI as fallback.
    c.publishPendingRawToMQTT(float64(snr), float64(rssi))

    if c.onControlData != nil {
        c.onControlData(cd)
    }
}

// case in handleFrame:
case pushCodeControlData:
    c.parseControlData(data)

// setter:
func (c *MeshCoreClient) OnControlData(f func(*ControlData)) {
    c.onControlData = f
}
```

---

### 0x1C (28) — `RESP_CODE_DEFAULT_FLOOD_SCOPE`

**Trigger:** Reply to `CMD_GET_DEFAULT_FLOOD_SCOPE` (not yet sent by bot). When a default scope is configured the payload includes the scope name and PSK; otherwise it's 1 byte only.

**Payload** (from `MyMesh.cpp:1910-1918`):
```
[0x1C]
[name: 31 bytes, null-padded]  — omitted if no scope configured (frame len == 1)
[psk:  16 bytes]               — omitted if no scope configured
```

**Implementation:** Add constant only for now; add parser and command when `CMD_GET_DEFAULT_FLOOD_SCOPE` is needed:
```go
respCodeDefaultFloodScope = 0x1C
```

---

## Priority 3 — Parse, log, and publish to MQTT

Every push code here is triggered by a received RF packet. The minimum viable implementation is: add a constant, add a case in `handleFrame` that logs at Debug level and calls `publishPendingRawToMQTT` with the SNR/RSSI available in the frame (or 0/0 if absent). An optional callback can be added later if the bot needs to act on the data.

### 0x84 — `PUSH_CODE_RAW_DATA`

**Trigger:** A raw (non-message) RF data packet arrived (e.g. from a sensor). Different from `0x88` (`pushCodeLogRxData`) which is a diagnostic copy of every received packet.

**Payload:**
```
[0x84]
[snr*4:  int8]
[rssi:   int8]
[0xFF]           reserved (may become path_len)
[payload: remaining bytes]
```

**Implementation:** Add constant `pushCodeRawData = 0x84`. Add a parser that decodes SNR/RSSI from bytes 1–2, logs at Debug level with hex payload, and calls `publishPendingRawToMQTT(snr, rssi)`. An optional `onRawData func(snr, rssi float32, payload []byte)` callback can be added later.

---

### 0x85 — `PUSH_CODE_LOGIN_SUCCESS`

**Trigger:** The bot sent a login request to a room server contact and the server accepted it. The response traveled over RF.

**Payload (legacy repeater):**
```
[0x85][0x00][pubkey_prefix: 6 bytes]
```
**Payload (v7+ server):**
```
[0x85][permissions: u8][pubkey_prefix: 6 bytes][server_timestamp: u32 LE][acl_perms: u8][fw_ver_level: u8]
```

**Implementation:** Add constant `pushCodeLoginSuccess = 0x85`. Log at Info level with pubkey prefix hex. Call `publishPendingRawToMQTT(0, 0)` (no SNR/RSSI in frame).

---

### 0x86 — `PUSH_CODE_LOGIN_FAIL`

**Trigger:** Login to room server was rejected. The rejection came over RF.

**Payload:**
```
[0x86][0x00][pubkey_prefix: 6 bytes]
```

**Implementation:** Add constant `pushCodeLoginFail = 0x86`. Log at Warn level with pubkey prefix hex. Call `publishPendingRawToMQTT(0, 0)`.

---

### 0x87 — `PUSH_CODE_STATUS_RESPONSE`

**Trigger:** Reply to a status query sent to a contact. Came over RF.

**Payload:**
```
[0x87][0x00][pubkey_prefix: 6 bytes][status_data: variable]
```

**Implementation:** Add constant `pushCodeStatusResponse = 0x87`. Log at Debug level. Call `publishPendingRawToMQTT(0, 0)`.

---

### 0x8B — `PUSH_CODE_TELEMETRY_RESPONSE`

**Trigger:** A contact responded to a telemetry request. Came over RF.

**Payload:**
```
[0x8B][0x00][pubkey_prefix: 6 bytes][telemetry_data: variable]
```

**Implementation:** Add constant `pushCodeTelemetryResponse = 0x8B`. Log at Debug level with hex payload. Call `publishPendingRawToMQTT(0, 0)`.

---

### 0x8C — `PUSH_CODE_BINARY_RESPONSE`

**Trigger:** A contact responded to a binary request; tag matches `RESP_CODE_SENT.tag`. Came over RF.

**Payload:**
```
[0x8C][0x00][tag: u32 LE][response_data: variable]
```

**Implementation:** Add constant `pushCodeBinaryResponse = 0x8C`. Log at Debug level. Call `publishPendingRawToMQTT(0, 0)`.

---

### 0x8D — `PUSH_CODE_PATH_DISCOVERY_RESPONSE`

**Trigger:** Path discovery to a contact completed (both in- and out-paths known). Came over RF.

**Payload:**
```
[0x8D][0x00][pubkey_prefix: 6 bytes]
[out_path_len: u8][out_path encoded via Packet::writePath]
[in_path_len:  u8][in_path  encoded via Packet::writePath]
```

**Implementation:** Add constant `pushCodePathDiscoveryResponse = 0x8D`. Log at Debug level. Call `publishPendingRawToMQTT(0, 0)`.

---

## Priority 4 — Response codes for commands the bot doesn't yet send

These can only arrive if the bot sends the corresponding command. Add constants now so future work can use them; no parser needed until the command is wired in.

| Hex | Dec | Constant name | Command that triggers it |
|-----|-----|---------------|--------------------------|
| 0x09 | 9 | `respCodeCurrTime` | `CMD_GET_DEVICE_TIME` |
| 0x0B | 11 | `respCodeExportContact` | `CMD_EXPORT_CONTACT` |
| 0x0E | 14 | `respCodePrivateKey` | `CMD_EXPORT_PRIVATE_KEY` |
| 0x0F | 15 | `respCodeDisabled` | generic "feature disabled" reply |
| 0x13 | 19 | `respCodeSignStart` | signing flow (`CMD_SIGN_DATA`) |
| 0x14 | 20 | `respCodeSignature` | signing flow (64-byte Ed25519 signature) |
| 0x16 | 22 | `respCodeAdvertPath` | `CMD_GET_ADVERT_PATH` |
| 0x17 | 23 | `respCodeTuningParams` | `CMD_GET_TUNING_PARAMS` |

Add all of these to `proto_consts.go` under the existing `// Response codes` block. In `handleFrame`, add a single grouped case that logs at Debug level:

```go
case respCodeCurrTime, respCodeExportContact, respCodePrivateKey,
     respCodeDisabled, respCodeSignStart, respCodeSignature,
     respCodeAdvertPath, respCodeTuningParams:
    c.logger.Debug("Unhandled response code (no command sender yet)",
        "code", fmt.Sprintf("0x%02x", data[0]), "len", len(data))
```

---

## Implementation order summary

| Step | Code(s) | What to do | Files |
|------|---------|-----------|-------|
| 0 | — | Extract `publishPendingRawToMQTT` helper; refactor `parseAdvert` to use it | `client.go` |
| 1 | 0x89 | Full parse + callback + MQTT publish; silences production WARN | `proto_consts.go`, `proto_models.go`, `client.go` |
| 2 | 0x1B | Full parse + callback + MQTT publish; enables binary channel data | `proto_consts.go`, `proto_models.go`, `client.go` |
| 3 | 0x8E | Full parse + callback + MQTT publish; enables mesh-side control interface | `proto_consts.go`, `proto_models.go`, `client.go` |
| 4 | 0x84–0x87, 0x8B–0x8D | Constants + Debug log + `publishPendingRawToMQTT` per case | `proto_consts.go`, `client.go` |
| 5 | 0x1C, 0x09, 0x0B, 0x0E, 0x0F, 0x13, 0x14, 0x16, 0x17 | Constants only + grouped Debug-log case | `proto_consts.go`, `client.go` |
