# last — recent channel history

Package `last` registers the `.last` command: returns the last *n* lines of a channel's
message history, read from the `historylog` files under `log/history/`.

## Command surface

```
.last               – last 3 messages of the current channel
.last <n>           – last n messages (n clamped to 1…7)
.last <n> <channel> – last n messages of another channel
```

Each returned entry is one message: `15:04 Sender: text`. Defaults: 3 messages, max 7
(the cap keeps the reply within mesh limits). Missing history →
`No history for channel "…"`.

## Configuration

None. Reads history from the shared `helpers.HistoryDir` (`log/history`), written by the
bot's history logger.
