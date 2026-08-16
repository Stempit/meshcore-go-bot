# stat — channel traffic (24h)

Package `stat` registers the `.stat` command: summarises the current channel's traffic
over the last 24 hours. **Channels only** — in a DM it replies
`Статистика доступна только в каналах`.

## Command surface

| Trigger | Match | Reply |
|---------|-------|-------|
| `.stat` | exact | message count, unique users, peak hour |

It reads the channel's `historylog`, excludes the bot's own (`Me`) messages and
anything older than 24h, then reports total messages, number of unique authors, and the
busiest clock hour (ties resolve to the earliest hour):

```
📊 Kaнaл зa 24ч:
✉️ 42 cooбщ
👥 7 yчacт
⏰ пик 20:00 (11)
```

## Configuration

None. Reads history from the shared `helpers.HistoryDir` (`log/history`).
