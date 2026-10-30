# commands

Self-registering command plugins — one folder per command, each owning its handler,
config, help text, and assets. There is no central switch or help list: `.help` and
routing are generated from each plugin's `Meta` by the
[`registry`](../support/registry/README.md) framework.

Adding a command = create `internal/commands/<name>/` implementing `registry.Command`
(+ `Configurable`/`Matcher` if needed) and add one blank import to
[`allcommands/`](allcommands/README.md). See [docs/NEW_COMMAND.md](../../docs/NEW_COMMAND.md).

## Plugins

| Folder | Trigger | Summary |
|--------|---------|---------|
| [`ping/`](ping/README.md) | `.ping` / `ping` / `пинг` | random pong |
| [`trace/`](trace/README.md) | `.trace` | hop count + distance from last ping |
| [`last/`](last/README.md) | `.last` | recent channel history |
| [`news/`](news/README.md) | `.news` | RSS feed items (from newswire) |
| [`llm/`](llm/README.md) | `.llm` | ask the LLM |
| [`weather/`](weather/README.md) | `.w` | current / next-day weather |
| [`path/`](path/README.md) | `.path` | best route between repeaters |
| [`top/`](top/README.md) | `.top` | most active channel authors |
| [`stat/`](stat/README.md) | `.stat` | channel traffic (24h) |
| [`eightball/`](eightball/README.md) | `.8ball` | magic 8-ball |
| [`ticket/`](ticket/README.md) | `.ticket` | stable random callsign |
| [`help/`](help/README.md) | `.help` | command index |
| [`agent/`](agent/README.md) | `.agent` / `@[Bot]` | your AI agent — nanobot or any OpenAI-compatible API (interceptor) |
| [`bbs/`](bbs/README.md) | `.2ch` | forum / BBS mode (stateful interceptor) |
| [`dice/`](dice/README.md) | `.dice` | roll dice / channel roll-off |
| [`num/`](num/README.md) | `.num` | guess the number |
| [`hang/`](hang/README.md) | `.hang` | hangman |
| [`words/`](words/README.md) | `.words` | anagram (Слова из слова) |
| [`vote/`](vote/README.md) | `.vote` | group poll |
| [`advert/`](advert/README.md) | `.advert` | advert parser (handled in dispatch, not a plugin) |
| [`allcommands/`](allcommands/README.md) | — | aggregator: blank-imports every plugin |

The text-game plugins (`dice`, `num`, `hang`, `words`) share the
[`support/games`](../support/games/README.md) layer.
