# greeter — greet newly seen nodes

Package `greeter` greets newly-seen nodes on the public channel as they appear in the
device's advert/contact stream. It is a **delivery-only** subsystem: it emits channel
messages through a caller-supplied `Sender` callback, so it has no dependency on the
outgoing-message pipeline ([dispatch](../dispatch/README.md)).

## How it works

```go
g := greeter.New(client, knownKeys, d.SendChannelMessage)
g.Register()   // installs client.OnAdvert / OnContact hooks
```

It hooks the MeshCore client's advert/contact events directly (it is event-driven, not a
text command — it never flows through command routing). When a node whose public key
isn't in the known-keys set appears, it sends a greeting and records the key.

## Persistence

Greeted node public keys are persisted (hex, one per line) in `log/known.txt`
(`known.go`), so a node is greeted only once across restarts.

## Configuration

None via `config.yaml`. The greeting text (`GreetMessage`) and the known-keys store are
supplied in code at construction from `cmd/bot`.
