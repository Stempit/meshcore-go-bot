# migrator — 1-byte → 2-byte hash nudge

Package `migrator` nudges mesh nodes still routing with **1-byte** hashes to switch to
2-byte. It is the reference [dispatch](../dispatch/README.md) `Observer`: it watches
received messages and replies **in kind** — a channel warning for a channel message, a
direct reply for a DM — using the dispatcher's delivery callbacks purely for sending
(never blocking).

## How it works

The routing hash size is read from each message's **real path** (bytes-per-hop =
`len(PathBytes) / PathLength`), never from stale advert routing (adverts don't carry the
node's current routing hash size — see the memory note on this gotcha). When a node is
seen on 1-byte hashes, the migrator queues the nudge, throttled per node.

```go
mig := migrator.New(chSender, dmSender, cfg)
d.AddObserver(mig)   // before d.Run
```

## Configuration

YAML key `Migration`.

```yaml
Migration:
  enabled: false
  message: "switch to 2-byte please"   # default
  cooldown_sec: 86400                  # min seconds between nudges per node (default 24h)
```
