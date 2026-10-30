# dispatch — the message engine

The `dispatch` package is the bot's message engine. It owns **both directions** of the
mesh-radio message flow and nothing else:

- **Inbound** — a worker pool that routes received DMs and channel messages to command
  handlers (`registry.Dispatch`), after parsing `.advert` and building `PingMeta`.
- **Outbound** — a single serialized sender that paces and retries transmissions,
  because the radio can only transmit one message at a time.

Business logic (greeting nodes, migration nudges) lives in its own package and plugs in;
the dispatcher never imports those packages — the dependency only points inward.

```
cmd/bot  ──▶ dispatch, greeter, migrator     (composition root wires them)
dispatch ──▶ commands(registry), config, meshcore, tgbridge, shrink
             (does NOT import greeter or migrator)
```

## Message flow

```
MeshCore device → client.IncomingMessages()
  → cmd/bot forwarder → d.Submit(msg)
  → d.incoming (buffered chan, cap 64)
  → runWorkers (semaphore, max 8 concurrent) → processDM / processChannelMessage
       ├─▶ observers (migrator, …)
       ├─▶ tgbridge.Forward (channel only)
       └─▶ registry.Dispatch → shrink.Fit
  → d.Enqueue(Outgoing) → d.outgoing (cap 64)
  → runSender (one at a time, paced) → client.Send…WithRetry → device
```

Both stored (replayed at startup) and live messages go through `Submit`, so handlers and
observers see them identically.

## Public API

```go
d := dispatch.New(client)
d.SetTelegram(tgb)   // optional: forward channel traffic to Telegram
d.AddObserver(mig)   // optional: watch inbound traffic (before Run)
d.Run(ctx)           // starts sender + worker goroutines, returns immediately
```

`Enqueue`/`TryEnqueue` queue an `Outgoing` (exactly one of `Contact` or `Channel` set).
`SendChannelMessage`/`SendDirectMessage` are the delivery callbacks handed to
subsystems (greeter, migrator, games announcer); they enqueue and drop on a full queue
rather than blocking.

## Concurrency model

- **Inbound is concurrent** — a goroutine per message, capped at 8, so a slow command
  (`.llm`, `.agent`) can't stall fast ones.
- **Outbound is serialized** — `runSender` drains one at a time, waiting ≥1s after
  activity (RX→TX switch) and ≥5s between radio sends.
- **One command in flight per sender** — a per-sender guard (`dm:<pubkey>` / `ch:<name>`)
  replies "busy" instead of queuing a second concurrent command. Observers run *before*
  this guard, so they see every message.

## Observers

Subsystems that **watch** traffic implement `Observer`
(`OnDirectMessage`/`OnChannelMessage`), satisfied structurally (no import of dispatch
needed). They must not block. The [`migrator`](../migrator/README.md) is the reference
implementation. Delivery-only subsystems (like [`greeter`](../greeter/README.md)) skip
`Observer` and just take a delivery callback. To add one: create a package that does not
import `dispatch`, take delivery callbacks at construction, and wire it in `cmd/bot`
before `d.Run`.

## Configuration

Reads `Connection.channel_transmissions` (via meshcore) and the central config; has no
config section of its own.
