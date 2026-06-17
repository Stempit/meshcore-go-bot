# The Message Engine (`internal/dispatch`)

The `dispatch` package is the bot's message engine. It owns **both directions**
of the mesh-radio message flow and nothing else:

- **Inbound** — a worker pool that routes received DMs and channel messages to
  command handlers ([`commands.GenerateReplies`](internal/commands/commands_server.go)).
- **Outbound** — a single serialized sender that paces and retries transmissions,
  because the radio can only transmit one message at a time.

Everything that is *business logic* (greeting new nodes, migration nudges, …)
lives in its own package and plugs into the engine. The dispatcher never imports
those packages; the dependency only points inward.

```
cmd/bot  ──▶ dispatch, greeter, migrator     (composition root wires them)
greeter  ──▶ commands, meshcore              (delivery via injected callback)
migrator ──▶ meshcore                        (delivery via injected callbacks)
dispatch ──▶ commands, config, meshcore, tgbridge, shrink
             (does NOT import greeter or migrator)
```

---

## Message flow

```
BLE device
   │  client.IncomingMessages()  (*meshcore.Message | *meshcore.ChannelMessage)
   ▼
cmd/bot live forwarder ──▶ d.Submit(msg)
   ▼
d.incoming (buffered chan, cap 64)
   ▼
runWorkers  ── semaphore (max 8 concurrent) ──▶ processDM / processChannelMessage
   │                                               │
   │                                               ├─▶ observers (migrator, …)
   │                                               ├─▶ tgbridge.Forward (channel only)
   │                                               └─▶ commands.GenerateReplies ─▶ shrink.Fit
   ▼
d.Enqueue(Outgoing{…})
   ▼
d.outgoing (buffered chan, cap 64)
   ▼
runSender  ── one at a time, paced ──▶ client.Send…WithRetry ──▶ BLE device
```

Both **stored** messages (replayed once at startup) and **live** messages go
through `Submit`, so handlers and observers see them identically.

---

## Public API

Construct, wire subsystems, then run:

```go
d := dispatch.New(client)
d.SetTelegram(tgb)        // optional: forward channel traffic to Telegram
d.AddObserver(mig)        // optional: watch inbound traffic (see below)
d.Run(ctx)                // starts the sender + worker goroutines, returns immediately
```

| Method | Purpose |
|---|---|
| `New(client)` | Build a dispatcher bound to the BLE client. |
| `SetTelegram(tgb)` | Attach a Telegram bridge for channel forwarding (`nil` disables). |
| `AddObserver(o)` | Register an `Observer` notified of every received message. Call before `Run`. |
| `Submit(msg)` | Feed an incoming `*meshcore.Message` / `*meshcore.ChannelMessage` into the pipeline. |
| `Enqueue(Outgoing)` | Queue a reply for transmission (blocks if the queue is full). |
| `TryEnqueue(Outgoing)` | Non-blocking enqueue; returns `false` if the queue is full. |
| `SendChannelMessage(ch, text, ack)` | Delivery callback: queue a channel message, dropping it if the queue is full. |
| `SendDirectMessage(contact, text, ack)` | Delivery callback: queue a DM, dropping it if the queue is full. |
| `Run(ctx)` | Start the inbound worker pool and outbound sender goroutines. |

### `Outgoing`

A reply to send back over the radio. **Exactly one** of `Contact` (DM) or
`Channel` (channel reply) must be non-nil:

```go
type Outgoing struct {
    Contact *meshcore.Contact   // set for a direct message
    Channel *meshcore.Channel   // set for a channel message
    Text    string
    Ack     bool                // request an end-to-end acknowledgement
}
```

---

## Concurrency model

- **Inbound is concurrent.** `runWorkers` spawns a goroutine per message, capped
  by a semaphore (`maxConcurrent = 8`), so a slow command (`.llm`, `.claw` — they
  block on the network) can't stall fast ones like `.ping`. A burst can't spawn
  unbounded goroutines.
- **Outbound is serialized.** `runSender` drains `d.outgoing` one message at a
  time. It waits ≥1s after activity (to let the device switch RX→TX) and enforces
  a ≥5s gap between consecutive radio sends. Channel and DM sends use different
  retry strategies (`SendChannelMessageWithRetry` vs `SendMessageWithRetry`, the
  latter falling back from ack→no-ack on failure).
- **One command in flight per sender.** A per-sender guard (`inflightSet`) lives
  inside the processors, keyed `dm:<pubkey>` / `ch:<name>`. A second command from
  the same sender while one is processing gets a "busy, try again" reply instead
  of queuing. Observer notifications run *before* this guard, so they fire for
  every message and never consume a command slot.

---

## Reacting to inbound traffic: `Observer`

Subsystems that **watch traffic** rather than answer commands implement
`Observer`. The dispatcher resolves the contact (DM) or channel before notifying,
so observers don't repeat the lookup:

```go
type Observer interface {
    OnDirectMessage(*meshcore.Message, *meshcore.Contact)
    OnChannelMessage(*meshcore.ChannelMessage, *meshcore.Channel)
}
```

Implementations **must not block** (they run on the worker goroutine, before
command handling). Do the cheap decision inline and hand any sending off to the
delivery callbacks, which only enqueue.

The [`migrator`](internal/migrator/migrator.go) package is the reference
implementation: on each received message it derives the routing hash size from
the message's real path (`len(PathBytes) / PathLength`) and, when a node is still
on 1-byte hashes, queues a "switch to 2-byte" nudge — replying *in kind* (channel
warning for a channel message, direct reply for a DM).

### Adding a new observer subsystem

1. Create a package under `internal/` that does **not** import `dispatch`.
2. Define a type with `OnDirectMessage` and `OnChannelMessage` methods (matching
   the `Observer` signatures structurally — no import needed to satisfy it).
3. Take delivery callbacks at construction so the subsystem can send without
   knowing about `dispatch.Outgoing`:

   ```go
   type ChannelSender func(ch *meshcore.Channel, text string, ack bool)
   type DMSender      func(contact *meshcore.Contact, text string, ack bool)
   ```

4. Wire it in `cmd/bot` (the composition root):

   ```go
   sub := mysub.New(/* deps */, d.SendChannelMessage, d.SendDirectMessage)
   d.AddObserver(sub)   // before d.Run(ctx)
   ```

This keeps the dependency one-directional: subsystems depend on the dispatcher's
delivery callbacks; the dispatcher never depends on the subsystems.

---

## Sending without observing: delivery-only subsystems

A subsystem that emits messages but doesn't react to inbound traffic (e.g.
[`greeter`](internal/greeter/greeter.go), which fires off the device's
advert/contact stream) skips `Observer` entirely. It just takes a delivery
callback:

```go
g := greeter.New(client, knownKeys, d.SendChannelMessage)
g.Register()   // installs its own client.OnAdvert / OnContact hooks
```

The Telegram bridge follows the same pattern from the other direction — its
`OnMessage` callback in `cmd/bot` calls `d.Enqueue` to push inbound Telegram
text onto the mesh.

---

## Where things live

| Concern | Location |
|---|---|
| Engine (in/out routing, pacing, retry) | [internal/dispatch/dispatch.go](internal/dispatch/dispatch.go) |
| Per-message command processing + `PingMeta` build | [internal/dispatch/process.go](internal/dispatch/process.go) |
| Command handlers | [internal/commands/](internal/commands/) (see [NEW_COMMAND.md](docs/NEW_COMMAND.md)) |
| 1-byte→2-byte migration nudge | [internal/migrator/](internal/migrator/) |
| Greeting new nodes | [internal/greeter/](internal/greeter/) |
| Reply length fitting | [internal/shrink/](internal/shrink/) (see [SHRINK.md](docs/SHRINK.md)) |
| Composition root (wires it all) | [cmd/bot/bot.go](cmd/bot/bot.go) |
