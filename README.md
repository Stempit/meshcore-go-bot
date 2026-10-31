# meshcore-go-bot 📻🤖

**Your off-grid mesh radio, but it talks back.**

*[🇷🇺 Русская версия](README_RU.md)*

meshcore-go-bot turns a [MeshCore](https://meshcore.io) LoRa device into a full-featured
chat bot that lives **on the mesh** — no cell tower, no Wi-Fi, no internet required for
the radio side. Ping it, ask it questions, play games with your neighbours, read the
news, check the weather, or route a message to Telegram — all over long-range radio, all
from a single tiny Go binary.

---

## Why you'll want it

- 📡 **Works where nothing else does.** Pure LoRa mesh. Off-grid campsites, dead zones,
  emergencies, field ops — if two MeshCore nodes can hear each other, meshcore-go-bot is there.
- 🧠 **AI on the mesh.** `.llm` and `.agent` bring an LLM / AI agent to a network with no
  internet in sight (the bot's host bridges out; the mesh stays radio-only).
- 🎮 **It's actually fun.** Dice roll-offs, hangman, guess-the-number, anagram races, and
  live polls — with 30-day leaderboards. Your channel becomes a game room.
- 🌉 **Bridges to the outside world.** Two-way Telegram, MQTT publishing to the
  [Let's Mesh](https://letsmesh.net) analyzer, RSS headlines, and live FPV/UAV & emergency
  hazard alerts pushed straight to a channel.
- 🛰️ **Engineered for bad radio.** Channel replies are transmitted multiple times with
  *adaptive early-stop* (it listens for the mesh echoing your packet and quits early),
  so messages actually arrive — something plain MeshCore channels never confirm.
- 🌍 **Bilingual.** Full English 🇬🇧 and Russian 🇷🇺 UI, switchable with one config key.
- 🧩 **Endlessly extensible.** Every command is a self-registering plugin — add a folder,
  add one import line, done. No central switch to edit.

---

## What it can do

Talk to it over mesh radio with simple `.commands`:

| | Command | Does |
|---|---|---|
| 🏓 | `.ping` | Are you there? (with personality) |
| 🛰️ | `.trace` | Hops + real distance to you |
| 📜 | `.last [n]` | Recent channel history |
| 📰 | `.news [feed]` | RSS headlines |
| 🧠 | `.llm <q>` | Ask an LLM |
| 🐾 | `.agent <q>` / `@Bot` | Ask your AI agent (nanobot or any OpenAI-compatible API) |
| ☀️ | `.w [город]` | Current / tomorrow's weather |
| 🗺️ | `.path A to B` | Best route between repeaters |
| 🏆 | `.top` · `.stat` | Channel activity & 24h traffic |
| 🎱 | `.8ball <q>` | Magic 8-ball |
| 🎟️ | `.ticket` | Your own stable callsign |
| 💬 | `.2ch` | A tiny forum/BBS, on the mesh |
| 🎲 | `.dice` `.num` `.hang` `.words` `.vote` | Games + polls with leaderboards |
| ❓ | `.help` | Discover everything |

Plus, running quietly in the background: greets new nodes, nudges legacy nodes to modern
routing, forwards Telegram ⇄ mesh, publishes to MQTT analyzers, and fires hazard alerts.

---

## Pros & cons

**Pros**
- ✅ Runs off-grid on LoRa mesh — no internet needed for radio operation
- ✅ Huge feature set out of the box: info, AI, games, bridges, alerts
- ✅ Real delivery reliability on a lossy, ACK-less medium
- ✅ Single static Go binary; connects over **Bluetooth LE, USB serial or TCP (Wi-Fi/Ethernet)**
- ✅ Bilingual (en/ru); replies tuned to the mesh's tight byte budget
- ✅ Clean plugin architecture — trivial to extend, well documented per package

**Cons** *(we're honest)*
- ⚠️ You need a MeshCore-compatible LoRa device to use it
- ⚠️ AI / weather / Telegram features need internet **on the bot's host**
- ⚠️ Language is bot-wide (not per-user); switching means an edit + restart
- ⚠️ Config is a single YAML loaded at startup — restart to apply changes
- ⚠️ Some games ship Russian-only word banks
- ⚠️ Replies are terse by design (140-byte mesh limit); the automated test suite is still thin

---

## Quick start

```bash
# from the repo root:
cp config.example.yaml config.yaml   # then fill in your device + secrets
go build ./cmd/bot
./bot                                # reads config.yaml in the current dir
# or grab a prebuilt binary from the Releases page (./release.sh builds them)
```

A minimal working `config.yaml` — everything else is optional:

```yaml
Connection:
  type: "bt"            # "bt" (Bluetooth LE, default), "serial" or "tcp" (Wi-Fi/Ethernet)
  # serial_port: "/dev/ttyUSB0"   # when type: "serial"
  # serial_baud: 115200
  # tcp_address: "192.168.1.50"   # when type: "tcp" — host or host:port (default 5000)
  # device_name: "MeshCore-AB12"  # when type: "bt" — pick one device by advertised name
  # device_address: "AA:BB:CC:DD:EE:FF"  # when type: "bt" — or by platform address

Language: "en"          # bot-wide reply language: "en" | "ru"

# Optional features — see config.example.yaml for every section:
#   Telegram bridge, MQTT / Let's Mesh analyzer, LLM (.llm), AI agent (.agent),
#   hazard alerts, weather, per-command settings…
```

Pair a MeshCore device over BLE (or point it at a serial port), drop into a channel, and
send `.help`.

---

## The meshcore family

meshcore-go-bot is the bot itself; the radio stack and the other device frontends live
in sibling repos:

| Repo | What it is |
|---|---|
| [meshcore-go](https://github.com/stempit/meshcore-go) | the protocol driver: BLE/serial/TCP, wire format, delivery-confirmed sends |
| [meshcore-kit](https://github.com/stempit/meshcore-kit) | shared app runtime (connect, sync, inbox, keepalive) + the 140-byte message fitter |
| [meshcore-http-gateway](https://github.com/stempit/meshcore-http-gateway) | your mesh behind a plain HTTP API (`/get`, `/send`, `/messages`) |
| [meshcore-mcp-gateway](https://github.com/stempit/meshcore-mcp-gateway) | your mesh as voice tools for the XiaoZhi assistant |

Each app opens its own device connection — don't point two of them at the same
physical radio at the same time.

---

## Learn more

- **Every package is documented** — see the [`internal/`](internal/README.md) index and
  each folder's own `README.md`.
- **Add your own command** in minutes: [`docs/NEW_COMMAND.md`](docs/NEW_COMMAND.md).
- **Architecture & conventions:** [`CLAUDE.md`](CLAUDE.md).

Built for the mesh. Get on the air. 📻
