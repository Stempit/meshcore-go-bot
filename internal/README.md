# internal

All of meshcore-go-bot's non-`main` code. Each subpackage has its own `README.md` covering its
purpose, abilities, and config options. The MeshCore protocol driver now lives in the
sibling repo [meshcore-go](https://github.com/stempit/meshcore-go); the shared device
runtime and the pure message fitter live in
[meshcore-kit](https://github.com/stempit/meshcore-kit).

## Layout

| Package | What it is |
|---------|------------|
| [`dispatch/`](dispatch/README.md) | the message engine: inbound worker pool + serialized outbound sender |
| [`commands/`](commands/README.md) | self-registering command plugins (one folder each) |
| [`support/`](support/README.md) | shared framework + support layer above `commands/` (`registry`, `games`, `helpers`, `historylog`) |
| [`bridge/`](bridge/README.md) | external integrations (Telegram, MQTT, LLM, universal agent client, hazard alerts) |
| [`greeter/`](greeter/README.md) | greets newly-seen nodes |
| [`migrator/`](migrator/README.md) | nudges 1-byte-hash nodes to switch to 2-byte |
| [`shrink/`](shrink/README.md) | config policy over the `fit` package (meshcore-kit): fits every outgoing reply into the 140-byte mesh limit |
| [`config/`](config/README.md) | YAML config loader (infra + per-plugin sections) |
| [`proxy/`](proxy/README.md) | shared HTTP client with optional SOCKS5 |
| [`logging/`](logging/README.md) | per-module `slog` logger factory |

Composition happens in `cmd/bot/bot.go`, which wires these together.
