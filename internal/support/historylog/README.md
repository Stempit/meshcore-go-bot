# historylog — per-conversation message logs

Package `historylog` persists incoming and outgoing mesh messages to per-conversation
text files under a configurable base directory. It backs `.last` / `.top` / `.stat` (via
`log/history/`) and the game leaderboards (via `log/games/<game>/`).

## Layout

```
<baseDir>/channels/<channel_name>.txt   – channel (group) messages
<baseDir>/dm/<key_prefix>.txt           – direct (private) messages
```

DM files are named after the contact's public-key prefix (hex) so both incoming and
outgoing messages for the same contact land in one file, regardless of whether the
contact's name was resolved at the time.

Each line is one message:

```
<< 2006-01-02 15:04:05 [SenderName]: message text
>> 2006-01-02 15:04:05 [Me]: message text
```

## API

A `Logger` bound to a base dir writes incoming/outgoing lines; `ReadLastChannel` /
`ReadAllChannel` read a channel's entries back as `[]Entry` (`Time`, `Sender`, `Text`).
The game leaderboards reuse the channel reader/writer with the game scope as the "channel
name" (DM scopes stored as `dm:<sender>`), so no separate DM namespace is needed.

## Configuration

None of its own — the base dir is supplied by the caller
(`helpers.HistoryDir` = `log/history`, or `games.BaseDir` = `log/games`).
