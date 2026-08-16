# tgbridge — Telegram bridge

Package `tgbridge` bridges MeshCore and Telegram in both directions:

- **Telegram → mesh:** long-polls configured Telegram `sources` (channels and group
  topics) and hands each new message to the caller (which enqueues it onto the mesh).
- **mesh → Telegram:** `Forward` sends a MeshCore channel message to the mapped
  Telegram `targets`.

## Outbound delivery

Forwarding to Telegram runs through a buffered queue (capacity 256) drained by a
**single worker**, so a slow or unreachable Telegram API can never block the mesh reply
path. A single worker preserves order; under a sustained outage the queue fills and
newer messages are dropped (logged) rather than queued unbounded. Each send gets up to 3
attempts with backoff, each bounded by its own 15s timeout.

## Configuration

YAML key `Telegram`.

```yaml
Telegram:
  enabled: true
  bot_token: "<bot token>"
  sources:                        # Telegram → mesh
    - type: channel               # "channel" | "group_thread"
      chat_id: -1001234567890
      # thread_id: 42             # required for group_thread
      mesh_channel: "#tg"
  targets:                        # mesh → Telegram
    - mesh_channel_name: "#tg"    # meshcore channel to listen on
      chat_id: -1001234567890     # Telegram chat/channel to post to
      thread_id: 0                # topic/thread id (0 = no thread)
```

A target's kind is implicit: a non-zero `thread_id` posts into a group topic; otherwise
it posts to a plain channel/group.

Mesh-fit shrinking of forwarded text (prefix `[TG:sender] `, `telegram` shrink variant)
is applied by [`internal/shrink`](../../shrink/README.md) at the send site.
