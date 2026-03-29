# MeshCore MQTT Protocol Specification

**Version:** 1.0  
**Last Updated:** 2024

This document describes the complete MQTT protocol implementation for MeshCore mesh networking devices. The protocol enables mesh packet transport over MQTT, supporting both standard TCP connections and WebSocket transports for firewall-friendly deployment.

## Table of Contents

1. [Overview](#overview)
2. [Transport Protocols](#transport-protocols)
3. [Topic Structure](#topic-structure)
4. [Message Types](#message-types)
5. [JSON Message Formats](#json-message-formats)
6. [Authentication](#authentication)
7. [Connection Configuration](#connection-configuration)
8. [CLI Commands Reference](#cli-commands-reference)
9. [Examples](#examples)

---

## Overview

MeshCore devices with MQTT bridge capability publish three types of messages:

| Message Type | Description | Retention | QoS |
|-------------|-------------|-----------|-----|
| **Status** | Device connection status and metadata | Retained | 1 |
| **Packets** | Full mesh packet data with RF characteristics | Non-retained | 1 |
| **Raw** | Minimal raw packet data for map integration | Non-retained | 1 |

### Key Features

- **Multiple broker support**: Up to 3 custom MQTT brokers
- **Let's Mesh Analyzer integration**: Built-in support for US and EU analyzer servers
- **Automatic reconnection**: Exponential backoff with configurable intervals
- **Packet queuing**: Up to 10 packets queued during connection issues
- **NTP time synchronization**: Accurate timestamps with timezone support
- **JWT authentication**: Ed25519-signed tokens for secure analyzer connections

---

## Transport Protocols

### 1. Standard TCP MQTT

```
mqtt://hostname:port
```

| Protocol | Port | TLS | Use Case |
|----------|------|-----|----------|
| `mqtt://` | 1883 | No | Local networks, development |
| `mqtts://` | 8884 | Yes | Production with TLS |

**Example:**
```
mqtt://broker.example.com:1883
mqtts://broker.example.com:8884
```

### 2. WebSocket MQTT (Recommended for Firewalls)

```
ws://hostname:port/mqtt
wss://hostname:port/mqtt
```

| Protocol | Port | TLS | Use Case |
|----------|------|-----|----------|
| `ws://` | 80 | No | Firewall-friendly, limited networks |
| `wss://` | 443 | Yes | Secure production deployment |

**Example:**
```
wss://broker.example.com:443/mqtt
```

### 3. Let's Mesh Analyzer Servers (WebSocket Only)

| Server | Endpoint | TLS |
|--------|----------|-----|
| US | `wss://mqtt-us-v1.letsmesh.net:443/mqtt` | Yes (GTS Root R4) |
| EU | `wss://mqtt-eu-v1.letsmesh.net:443/mqtt` | Yes (GTS Root R4) |

**Note:** Let's Mesh Analyzer uses JWT authentication instead of username/password.

---

## Topic Structure

All topics follow the pattern:

```
meshcore/{IATA}/{DEVICE_PUBLIC_KEY}/{MESSAGE_TYPE}
```

### Components

| Component | Description | Example |
|-----------|-------------|---------|
| `IATA` | Airport code (uppercase) | `SEA`, `LAX`, `LHR` |
| `DEVICE_PUBLIC_KEY` | Device public key (64 hex chars) | `7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400` |
| `MESSAGE_TYPE` | Message type identifier | `status`, `packets`, `raw` |

### Topic Examples

```
meshcore/SEA/7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400/status
meshcore/SEA/7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400/packets
meshcore/SEA/7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400/raw
```

---

## Message Types

### Status Message

Device connection status published periodically (default: every 5 minutes).

**Topic:** `meshcore/{IATA}/{DEVICE_PUBLIC_KEY}/status`  
**QoS:** 1  
**Retained:** Yes

**Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | Connection status: `"online"` or `"offline"` |
| `timestamp` | string | ISO 8601 timestamp (local time with microseconds) |
| `origin` | string | Device name/origin identifier |
| `origin_id` | string | Device public key (64 hex characters) |
| `model` | string | Device hardware model |
| `firmware_version` | string | Firmware version string |
| `radio` | string | Radio parameters: `frequency,bw,sf,cr` |
| `client_version` | string | Client version: `meshcore/{firmware_version}` |
| `stats` | object | (optional) Device statistics |

**Stats Object Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `battery_mv` | int | Battery voltage in millivolts |
| `uptime_secs` | int | Device uptime in seconds |
| `errors` | int | Error flags |
| `queue_len` | int | MQTT packet queue length |
| `noise_floor` | int | Radio noise floor in dBm |
| `tx_air_secs` | int | TX air time in seconds |
| `rx_air_secs` | int | RX air time in seconds |
| `recv_errors` | int | Radio receive errors |

### Packet Message

Mesh packet data with full metadata.

**Topic:** `meshcore/{IATA}/{DEVICE_PUBLIC_KEY}/packets`  
**QoS:** 1  
**Retained:** No

**Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `origin` | string | Device name that captured the packet |
| `origin_id` | string | Capturing device's public key |
| `timestamp` | string | ISO 8601 timestamp (local time) |
| `type` | string | Message type: `"PACKET"` |
| `direction` | string | Direction: `"rx"` or `"tx"` |
| `time` | string | UTC time: `HH:MM:SS` |
| `date` | string | UTC date: `DD/MM/YYYY` |
| `len` | string | Total packet length in bytes |
| `packet_type` | string | Packet type code (0-15) |
| `route` | string | Route type: `F` (FLOOD), `D` (DIRECT), `T` (TRANSPORT), `U` (UNKNOWN) |
| `payload_len` | string | Payload length in bytes |
| `raw` | string | Raw packet data (hex encoded) |
| `SNR` | string | Signal-to-noise ratio (dB) |
| `RSSI` | string | Received signal strength (dBm) |
| `hash` | string | Packet hash (32 hex characters) |
| `path` | string | (optional) Routing path for direct packets |

**Packet Type Codes:**

| Code | Type | Description |
|------|------|-------------|
| 0 | REQ | Request |
| 1 | RESPONSE | Response |
| 2 | TXT_MSG | Text message |
| 3 | ACK | Acknowledgment |
| 4 | ADVERT | Advertisement |
| 5 | GRP_TXT | Group text |
| 6 | GRP_DATA | Group data |
| 7 | ANON_REQ | Anonymous request |
| 8 | PATH | Path information |
| 9 | TRACE | Trace route |
| 10 | MULTIPART | Multi-part message |

### Raw Message

Minimal raw packet data for map integration.

**Topic:** `meshcore/{IATA}/{DEVICE_PUBLIC_KEY}/raw`  
**QoS:** 1  
**Retained:** No

**Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `origin` | string | Device name that captured the packet |
| `origin_id` | string | Capturing device's public key |
| `timestamp` | string | ISO 8601 timestamp (local time) |
| `type` | string | Message type: `"RAW"` |
| `data` | string | Raw packet data (hex encoded) |

---

## JSON Message Formats

### Status Message Example

```json
{
  "status": "online",
  "timestamp": "2024-01-15T14:30:00.000000",
  "origin": "MeshCore-Repeater-01",
  "origin_id": "7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400",
  "model": "Heltec_Wireless_Track",
  "firmware_version": "v1.2.3",
  "radio": "915.000000,250.0,10,5",
  "client_version": "meshcore/v1.2.3",
  "stats": {
    "battery_mv": 4200,
    "uptime_secs": 86400,
    "errors": 0,
    "queue_len": 0,
    "noise_floor": -120,
    "tx_air_secs": 120,
    "rx_air_secs": 360,
    "recv_errors": 2
  }
}
```

### Packet Message Example (Flood Route)

```json
{
  "origin": "MeshCore-Repeater-01",
  "origin_id": "7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400",
  "timestamp": "2024-01-15T14:30:05.123456",
  "type": "PACKET",
  "direction": "rx",
  "time": "14:30:05",
  "date": "15/01/2024",
  "len": "45",
  "packet_type": "4",
  "route": "F",
  "payload_len": "32",
  "raw": "F5930103807E5F1E2A3B4C5D6E7F8091A2B3C4D5E6F708192A3B4C5D6E7F8091A2B3",
  "SNR": "12.5",
  "RSSI": "-65",
  "hash": "A1B2C3D4E5F6789012345678"
}
```

### Packet Message Example (Direct Route)

```json
{
  "origin": "MeshCore-Repeater-01",
  "origin_id": "7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400",
  "timestamp": "2024-01-15T14:30:10.234567",
  "type": "PACKET",
  "direction": "tx",
  "time": "14:30:10",
  "date": "15/01/2024",
  "len": "67",
  "packet_type": "2",
  "route": "D",
  "payload_len": "54",
  "raw": "F5930203807E5F1E2A3B4C5D6E7F8091A2B3C4D5E6F708192A3B4C5D6E7F8091A2B3",
  "SNR": "15.2",
  "RSSI": "-58",
  "hash": "B2C3D4E5F67890123456789",
  "path": "path_2x6_12b"
}
```

### Raw Message Example

```json
{
  "origin": "MeshCore-Repeater-01",
  "origin_id": "7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400",
  "timestamp": "2024-01-15T14:30:15.345678",
  "type": "RAW",
  "data": "F5930402807E5F1E2A3B4C5D6E7F8091A2B3C4D5E6F708192A3B4C5D6E7F8091A2B3C4D5E6F708192A"
}
```

---

## Authentication

### 1. Username/Password Authentication (Custom Brokers)

Standard MQTT username/password authentication for custom broker connections.

```cpp
// Example: Setting credentials
_mqtt_client->setCredentials("username", "password");
```

**Note:** Credentials are optional for anonymous connections.

### 2. JWT Authentication (Let's Mesh Analyzer)

Let's Mesh Analyzer uses JWT tokens signed with Ed25519 keys for authentication.

**Username Format:**
```
v1_{UPPERCASE_PUBLIC_KEY}
```

**Example:**
```
v1_7E7662676F7F0850A8A355BAAFBFC1EB7B4174C340442D7D7161C9474A2C9400
```

**Token Claims:**

| Claim | Description |
|-------|-------------|
| `iss` | Issuer: `mqtt-us-v1.letsmesh.net` or `mqtt-eu-v1.letsmesh.net` |
| `sub` | Subject: Device public key |
| `iat` | Issued at: Unix timestamp |
| `exp` | Expires: Unix timestamp (24 hours from issue) |
| `owner` | (optional) Owner public key |
| `client` | Client version string |
| `email` | (optional) Owner email |

**Token Generation:**
```cpp
// Uses device Ed25519 identity for signing
JWTHelper::createAuthToken(identity, server_name, issued_at, expires_in, 
                           token_buffer, token_size, owner_key, client_version, email);
```

### 3. TLS Certificate Authentication

Custom brokers can use TLS with certificate authentication.

```cpp
// CA Certificate
_mqtt_client->setCACert(ca_certificate_pem);

// Client certificate (mutual TLS)
_mqtt_client->setClientCertificate(client_cert, client_key);
```

**Let's Mesh Analyzer CA:** Google Trust Services - GTS Root R4

---

## Connection Configuration

### Client Settings

| Setting | Default | Description |
|---------|---------|-------------|
| Keep-alive | 45 seconds | Prevents WebSocket proxy timeouts |
| Buffer size | 896 bytes | MQTT send/receive buffer |
| QoS | 1 | Quality of service level |
| Auto-reconnect | Enabled | Automatic reconnection on disconnect |

### Reconnection Strategy

**Main Broker (Custom):**

| Attempt | Delay |
|---------|-------|
| 1 | 15 seconds |
| 2 | 30 seconds |
| 3 | 60 seconds |
| 4 | 120 seconds |
| 5 | 300 seconds |

**Analyzer Servers:**

| Attempt | Delay |
|---------|-------|
| 1 | 60 seconds |
| 2 | 120 seconds |
| 3 | 240 seconds |
| 4+ | 300 seconds |

### Memory Management

- **PSRAM utilization**: Large buffers allocated in PSRAM when available
- **Fragmentation recovery**: Automatic client recreation when heap is fragmented
- **Packet queuing**: Up to 10 packets queued during connection issues
- **Adaptive delays**: Shorter delays when work pending, longer when idle

---

## CLI Commands Reference

### MQTT Configuration

| Command | Description |
|---------|-------------|
| `get mqtt.origin` | Get device origin name |
| `set mqtt.origin <name>` | Set device origin name |
| `get mqtt.iata` | Get IATA airport code |
| `set mqtt.iata <code>` | Set IATA airport code |
| `get mqtt.status` | Get status message setting |
| `set mqtt.status on\|off` | Enable/disable status messages |
| `get mqtt.packets` | Get packet message setting |
| `set mqtt.packets on\|off` | Enable/disable packet messages |
| `get mqtt.raw` | Get raw message setting |
| `set mqtt.raw on\|off` | Enable/disable raw messages |
| `get mqtt.tx` | Get TX message setting |
| `set mqtt.tx on\|off` | Enable/disable TX packet messages |
| `get mqtt.interval` | Get status publish interval |
| `set mqtt.interval <ms>` | Set status publish interval (ms) |
| `get mqtt.server` | Get MQTT server hostname |
| `set mqtt.server <host>` | Set MQTT server hostname |
| `get mqtt.port` | Get MQTT server port |
| `set mqtt.port <port>` | Set MQTT server port |
| `get mqtt.username` | Get MQTT username |
| `set mqtt.username <user>` | Set MQTT username |
| `get mqtt.password` | Get MQTT password |
| `set mqtt.password <pwd>` | Set MQTT password |
| `get mqtt.analyzer.us` | Get US analyzer setting |
| `set mqtt.analyzer.us on\|off` | Enable/disable US analyzer |
| `get mqtt.analyzer.eu` | Get EU analyzer setting |
| `set mqtt.analyzer.eu on\|off` | Enable/disable EU analyzer |
| `get mqtt.owner` | Get owner public key |
| `set mqtt.owner <hex>` | Set owner public key (64 hex chars) |
| `get mqtt.email` | Get owner email |
| `set mqtt.email <email>` | Set owner email |
| `get mqtt.config.valid` | Check if MQTT config is valid |

### WiFi Configuration

| Command | Description |
|---------|-------------|
| `get wifi.ssid` | Get WiFi SSID |
| `set wifi.ssid <ssid>` | Set WiFi SSID |
| `get wifi.pwd` | Get WiFi password |
| `set wifi.pwd <password>` | Set WiFi password |
| `get wifi.status` | Get WiFi connection status |
| `get wifi.powersave` | Get WiFi power save mode |
| `set wifi.powersave none\|min\|max` | Set WiFi power save mode |

### Timezone Configuration

| Command | Description |
|---------|-------------|
| `get timezone` | Get timezone string |
| `set timezone <tz>` | Set timezone (IANA/abbreviation/offset) |
| `get timezone.offset` | Get timezone offset |
| `set timezone.offset <offset>` | Set timezone offset (-12 to +14) |

**Supported Timezone Formats:**
- IANA: `America/Los_Angeles`, `Europe/London`, `Asia/Tokyo`
- Abbreviations: `PDT`, `PST`, `EST`, `EDT`, `GMT`, `CET`
- Offsets: `UTC-8`, `UTC+5`, `+5`, `-8`

### Bridge Configuration

| Command | Description |
|---------|-------------|
| `get bridge.enabled` | Get bridge enabled status |
| `set bridge.enabled on\|off` | Enable/disable bridge |
| `get bridge.source` | Get packet source (rx/tx) |
| `set bridge.source rx\|tx` | Set packet source |

---

## Examples

### 1. Connecting to Custom MQTT Broker (TCP)

```bash
# Configure WiFi first
set wifi.ssid "MyNetwork"
set wifi.pwd "MyPassword"

# Set device identity
set mqtt.origin "MyRepeater"
set mqtt.iata "LAX"

# Configure MQTT broker
set mqtt.server "broker.example.com"
set mqtt.port 1883
set mqtt.username "myuser"
set mqtt.password "mypassword"

# Enable features
set mqtt.status on
set mqtt.packets on
set mqtt.raw off

# Configure bridge
set bridge.enabled on
set bridge.source rx

# Verify
get mqtt.config.valid
```

### 2. Using Let's Mesh Analyzer Only (WebSocket)

```bash
# Basic WiFi configuration
set wifi.ssid "MyNetwork"
set wifi.pwd "MyPassword"

# Set IATA code for topic structure
set mqtt.iata "SFO"

# Let's Mesh Analyzer is enabled by default
get mqtt.analyzer.us
get mqtt.analyzer.eu

# Configure bridge to capture RX packets
set bridge.source rx
set bridge.enabled on

# Reboot to apply changes
reboot
```

### 3. Subscribing to Messages (Python)

```python
import paho.mqtt.client as mqtt
import json

def on_connect(client, userdata, flags, rc):
    print(f"Connected with result code {rc}")
    # Subscribe to all messages for a specific device
    client.subscribe("meshcore/SEA/+/packets")
    client.subscribe("meshcore/SEA/+/status")
    client.subscribe("meshcore/SEA/+/raw")

def on_message(client, userdata, msg):
    topic = msg.topic
    payload = json.loads(msg.payload)
    
    if "packets" in topic:
        print(f"Packet: {payload['direction']} {payload['route']} "
              f"SNR={payload['SNR']} RSSI={payload['RSSI']}")
    elif "status" in topic:
        print(f"Status: {payload['status']} Uptime: {payload.get('stats', {}).get('uptime_secs', 'N/A')}s")
    elif "raw" in topic:
        print(f"Raw: {len(payload['data'])} bytes")

client = mqtt.Client()
client.on_connect = on_connect
client.on_message = on_message

client.connect("broker.example.com", 1883, 60)
client.loop_forever()
```

### 4. Using WebSocket Transport

```bash
# For firewalls that block direct MQTT (port 1883)
# Configure broker with WebSocket endpoint
set mqtt.server "broker.example.com"
set mqtt.port 80  # WebSocket port (non-TLS)

# Note: Full WebSocket URL path (/mqtt) is handled automatically
```

### 5. Complete Configuration Example

```bash
# WiFi Settings
set wifi.ssid "HomeNetwork"
set wifi.pwd "SecurePassword123"
set wifi.powersave min

# Device Identity
set mqtt.origin "Backyard-Repeater"
set mqtt.iata "PDX"

# Timezone (Pacific Time with DST)
set timezone "America/Los_Angeles"

# MQTT Broker (optional custom broker)
set mqtt.server "mqtt.mybroker.com"
set mqtt.port 8883
set mqtt.username " repeater01"
set mqtt.password "brokerpassword"

# Message Settings
set mqtt.status on          # Enable status messages
set mqtt.packets on         # Enable packet messages
set mqtt.raw off            # Disable raw messages
set mqtt.tx off             # Only RX packets
set mqtt.interval 300000    # Status every 5 minutes

# Let's Mesh Analyzer (enabled by default)
get mqtt.analyzer.us        # Should be "on"
get mqtt.analyzer.eu        # Should be "on"

# Bridge Configuration
set bridge.enabled on       # Enable bridge
set bridge.source rx        # Capture received packets

# Save and restart
reboot
```

### 6. mosquitto_sub Commands

```bash
# Subscribe to all devices in a region
mosquitto_sub -h broker.example.com -p 1883 -u user -P pass \
  -t "meshcore/SEA/+/packets" -t "meshcore/SEA/+/status"

# Subscribe with JSON formatting
mosquitto_sub -h broker.example.com -p 1883 -u user -P pass \
  -t "meshcore/SEA/+/packets" | jq .

# Subscribe with verbose output (include topic)
mosquitto_sub -h broker.example.com -p 1883 -u user -P pass \
  -t "meshcore/SEA/#" -v

# Subscribe using WebSocket
mosquitto_sub -h broker.example.com -p 80 \
  -P "/mqtt" -t "meshcore/SEA/+/packets"
```

### 7. Python MQTT Client with TLS

```python
import paho.mqtt.client as mqtt
import ssl

def on_connect(client, userdata, flags, rc):
    if rc == 0:
        print("Connected to MQTT broker")
        client.subscribe("meshcore/+/+/packets")
    else:
        print(f"Connection failed with code {rc}")

client = mqtt.Client()
client.tls_set(ca_certs="/path/to/ca.crt")
client.tls_cert_reqs = ssl.CERT_REQUIRED
client.tls_insecure = False  # Set True only for testing
client.on_connect = on_connect

client.username_pw_set("username", "password")
client.connect("broker.example.com", 8883, 60)
client.loop_forever()
```

---

## Appendix: Error Codes

### Connection States

| State | Description |
|-------|-------------|
| `disconnected` | WiFi/MQTT not connected |
| `connected` | Connected to WiFi and MQTT broker |

### WiFi Status Codes

| Code | Description |
|------|-------------|
| `no_ssid` | SSID not available |
| `connect_failed` | Connection attempt failed |
| `connection_lost` | Connection lost |
| `disconnected` | Manually disconnected |

### Bridge Status Response

```
> msgs: on, broker: connected, us: connected, eu: connected, queue: 0
```

---

## Revision History

| Version | Date | Changes |
|---------|------|---------|
| 1.0 | 2024-01 | Initial specification |

---

## See Also

- [MQTT Implementation Guide](./MeshCore/MQTT_IMPLEMENTATION.md)
- [MeshCore Documentation](./MeshCore/docs/)
- [Let's Mesh Analyzer](https://letsmesh.net)
