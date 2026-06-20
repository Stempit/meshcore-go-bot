# meshbot

A MeshCore mesh-radio bot. Bridges a Bluetooth LE MeshCore device to Telegram, MQTT brokers, RSS feeds, and an LLM. Responds to `.ping`, `.trace`, `.last`, `.llm`, and `.news` commands over mesh radio.

## Build & run

```bash
go build ./...                    # build everything
go build ./cmd/bot                # build the main bot

go run ./cmd/bot                  # run the bot (reads config.yaml in CWD)
go run ./cmd/example              # run the BLE example harness
go run ./cmd/llm                  # run the LLM prompt test utility

go vet ./...                      # static analysis
```

There are no automated tests. Manual testing uses `cmd/example` and `cmd/llm`.

## Architecture

```
cmd/bot/bot.go          – main entry point; wires everything together
internal/
  meshcore_bt/          – BLE protocol driver (client.go is the main type)
  commands/             – command handlers: ping, trace, last, news, llm, greet
  games/                – text games: dice, num (guess-the-number), hang (hangman)
  config/               – YAML config loader (singleton via sync.Once)
  historylog/           – per-conversation append-only log files
  llm/                  – Ollama and OpenRouter LLM providers
  mqttbridge/           – MQTT publishing to custom broker + Let's Mesh analyzer
  tgbridge/             – Telegram bot bridge (long-poll)
```

The MeshCore BLE client (`internal/meshcore_bt/client.go`) is the core dependency. Everything else depends on it.

Message flow: BLE device → `client.IncomingMessages()` channel → `processDM` / `processChannelMessage` in `bot.go` → `commands.GenerateReplies` → reply sent back over BLE.

## Configuration

Copy `config.example.yaml` to `config.yaml` and fill in secrets. The file is loaded once at startup; restart to pick up changes.

Key environment variables:
- `OPENROUTER_API_KEY` – required when `LLM.type: openrouter`
- `OLLAMA_HOST` – Ollama base URL (default: `http://localhost:11434`)

**Note:** the yaml key for LLM shrink retries is `shrink_max_retries` (fixed from the original typo `shrink_max_reties`).

## Key conventions

- **All log messages in English.** Use `log/slog` with structured key-value pairs.
- **No `log.Fatal` inside packages.** Return errors to callers; only `os.Exit` in `main`.
- **No `goto`.** Use labeled `break` for loop exits.
- **Signal handling via `signal.NotifyContext`.** Pass `ctx.Done()` to goroutines instead of a bare `done` channel.
- **Command prefix stripping:** use `strings.TrimPrefix` + `strings.TrimSpace`, not `strings.ReplaceAll`.
- **`bytes.Equal` from stdlib**, not a hand-rolled helper.
- Config is accessed via `config.Get()` everywhere; it is a pointer to a value struct — treat it as read-only.

## Runtime file layout

```
config.yaml
log/
  history/
    channels/<name>.txt   – channel message history (used by .last)
    dm/<name>.txt         – direct-message history
  known.txt               – greeted node public keys (hex, one per line)
  news/<feed>.txt         – RSS items written by the separate newswire app (https://github.com/Stempit/newswire); read by .news
```

## Packages that need care

- **`internal/meshcore_bt/client.go`** (~2300 lines) – BLE protocol state machine. The `sentConfirmCh` channel is a single shared slot; only one send can be in flight at a time, so concurrent senders must serialise their sends.
- **`internal/mqttbridge/bridge.go`** (~1700 lines) – manages multiple MQTT connections (custom broker + EU/US analyzers) with JWT auth (Ed25519). Changes here affect network-visible state.
- **`internal/config/model.go`** – adding a new config field requires a YAML tag; check for typos before committing.

## What the commands do

| Command | Handler | Notes |
|---------|---------|-------|
| `ping` / `пинг` / `.ping` | `commands.ProcessPingMessage` | Random pong string |
| `.trace` | `commands.ProcessTraceMessage` | Haversine distance + hop IDs from `PingMeta` |
| `.last [n] [channel]` | `commands.ProcessLastCommand` | Last *n* lines from `historylog` (max 7) |
| `.llm <prompt>` | `commands.ProcessLLMMessage` | LLM query; auto-shrinks response to ≤130 chars |
| `.news [feed] [n] [offset]` | `commands.ProcessNewsCommand` | Lists feeds or returns recent RSS items from `log/news/`, populated by the [newswire](https://github.com/Stempit/newswire) poller |
| `.claw <prompt>` | `commands.ProcessClawCommand` | Sends prompt to picoclaw AI agent via WebSocket; auto-shrinks response |
| `.8ball <question>` | `commands.ProcessEightBallCommand` | Magic 8-Ball; random Russian yes/no/maybe answer |
| `.top` | `commands.ProcessTopCommand` | Top-5 channel authors by message count (scans last `Top.scan_count`, default 100); channels only, "not available" in DMs |
| `.stat` | `commands.ProcessStatCommand` | Channel traffic in last 24h: message count, peak hour, unique users; channels only, "not available" in DMs |
| `.dice [NdM]` | `games.ProcessDiceCommand` | Roll dice (`NdM`/`M`, default `1d6`). Sub-verbs `start`/`roll`/`end`/`status` run a per-channel roll-off (highest total wins; one roll per player; stays open until anyone runs `.dice end`); session verbs channel-only |
| `.num [start/<n>/status/end/top]` | `games.ProcessNumCommand` | Guess-the-number (bot picks 1..max, default 100; `больше`/`меньше` hints). Works in channels (shared) and DMs (private); `.num top` is a 30-day win leaderboard. See [docs/games/NUM.md](docs/games/NUM.md) |
| `.hang [start/<буква>/<слово>/status/end/top]` | `games.ProcessHangCommand` | Hangman in Russian from an embedded word bank (`words_ru.txt`, ё→е, 6 lives). Channels are collaborative, DMs solo; `.hang top` is a 30-day leaderboard. See [docs/games/HANG.md](docs/games/HANG.md) |
| `.words [start/<слово>/status/end/top]` | `games.ProcessWordsCommand` | Anagram ("Слова из слова"): timed round, build words from one long base word's letters; `score = len²`. Opt-in (`words.enabled`), runtime dict `words_anagram_ru.txt`. First game with a self-firing timer that pushes results via the injected `games.Announcer`. Channels collaborative-competitive, DMs solo; `.words top` is a 30-day leaderboard. See [docs/games/WORDS.md](docs/games/WORDS.md) |

New commands go in `internal/commands/` (or `internal/games/` for games) and must be wired into the `switch` in `commands_server.go`.

The `games` package keeps game logic separate from the command dispatcher; `commands` imports it. Win records and leaderboards (`.num top` / `.hang top`) reuse the `historylog` package under `log/games/<game>/`, keyed by game scope — see [docs/games/](docs/games/).
