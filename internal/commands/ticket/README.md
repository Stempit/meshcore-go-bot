# ticket — stable random callsign

Package `ticket` registers the `.ticket` command: assigns each sender a stable, random
two-word "callsign" (radio-themed Russian words, e.g. `Искрящий Трансивер`) and persists
it, so the same sender always gets the same phrase. Not listed in the `.help` index.

## Command surface

```
.ticket          – your callsign (created and persisted on first use)
.ticket random   – pick a random already-issued callsign
```

The callsign is keyed by `SenderKey`. DM keys (raw public-key bytes) are hex-encoded for
a printable, collision-safe filename key; channel names pass through. With no sender key
the reply asks the user to send an advert first.

## Persistence

Callsigns are stored one per line (`key\tphrase`) in the file passed to
`InitTicketStore(path)` at startup (wired from `cmd/bot`). The store is loaded once and
appended to as new senders appear.

## Configuration

None (the store path is set in code at startup, not via `config.yaml`).
