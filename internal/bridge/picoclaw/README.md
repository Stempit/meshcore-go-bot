# picoclaw — AI agent WebSocket client

Package `picoclaw` is a thin client for a [picoclaw](https://github.com) AI agent
gateway, spoken over the Pico Protocol WebSocket. It is used by the
[`.claw`](../../commands/claw/README.md) command.

## API

```go
parts, err := picoclaw.Ask(ctx, wsURL, token, sessionID, text)
```

- `wsURL` must point at the picoclaw **gateway** (default port 18790), e.g.
  `ws://host:18790/pico/ws`. The web dashboard port (18800) uses session-cookie auth and
  cannot be used here.
- `token` — the Pico token from the picoclaw dashboard.
- `sessionID` — should be stable per user/conversation so history is maintained. The
  `.claw` command derives it from the channel (shared) or the DM sender.
- Returns the agent's response parts (one element per visible message the agent emits;
  usually one, but multi-message turns are preserved). Cancel/timeout via `ctx`.

The `session_id` is passed as a query parameter so the gateway routes responses back to
this connection.

## Configuration

None here — connection details (`url`, `token`, `timeout_sec`) come from the
[`Claw`](../../commands/claw/README.md) config section, passed in by the caller.
