# top — most active channel authors

Package `top` registers the `.top` command: ranks the most active authors in the
channel where it was called, by message count. **Channels only** — in a DM it replies
`Топ доступен только в каналах`.

## Command surface

| Trigger | Match | Reply |
|---------|-------|-------|
| `.top` | exact | top-3 authors with bar chart |

It reads the channel's `historylog`, drops the bot's own (`Me`) messages, keeps the
most recent `scan_count` messages, tallies per author, and returns the top 3 with medal
icons and a fixed-width `▇` bar scaled to the leader. Author names are clipped to 12
runes so the line stays within the 140-byte mesh limit.

## Configuration

YAML key `Top`.

```yaml
Top:
  scan_count: 100     # recent messages scanned (default 100)
```
