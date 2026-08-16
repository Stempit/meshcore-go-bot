# claw — picoclaw AI agent

Package `claw` registers the `.claw` command and an `@[BotName]` mention interceptor,
both routed to the [picoclaw](../../bridge/picoclaw/README.md) AI agent over WebSocket.
Responses are auto-shrunk at the send site (the `claw` split override).

## Command surface

```
.claw <вопрос>       – ask the picoclaw agent
@[BotName] <вопрос>  – same, via a mention (when configured)
```

`claw` is a `Matcher` interceptor at **Priority 20** — above bbs (10) — so an
`@[BotName]` mention beats an active BBS session. The mention form is rewritten to a
`.claw` prompt before handling.

Sessions: channel messages share one picoclaw session per channel; DMs get one per
sender (keys hex-encoded to survive JSON). Empty prompt → usage line; unconfigured →
`picoclaw не настроен`; connection error → `ошибка соединения с picoclaw`.

## Configuration

YAML key `Claw`. The `.claw` trigger is always routed (so it can reply
"not configured"); the `@[BotName]` mention matcher additionally requires `enabled` and
a non-empty `bot_name`.

```yaml
Claw:
  enabled: false
  url: "ws://host:18790/pico/ws"   # gateway port, not the dashboard (18800)
  token: "<pico token>"            # from the picoclaw dashboard
  timeout_sec: 180
  bot_name: "Yorick 💀"            # mesh display name, for @[BotName] mentions
```
