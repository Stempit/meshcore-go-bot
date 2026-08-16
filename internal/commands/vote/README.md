# vote — group poll

Package `vote` registers the `.vote` command: a per-channel poll. One sender opens a
question with a list of options, everyone casts one anonymous, changeable vote, and
results are posted when the timer fires or anyone runs `.vote end`.

**Channels only.** Any session verb in a DM replies
`Голосование доступно только в каналах`. State is keyed by channel name (one active
poll per channel).

## Command surface

| Input | Action |
|-------|--------|
| `.vote start вопрос \| вариант1 \| вариант2 [...]` | Open a poll; pipe-separated first segment = question, rest = options (2–`max_options`) |
| `.vote <n>` | Cast or change this sender's single vote to option *n* (1-based) |
| `.vote status` | Show time left and current tally |
| `.vote end` | Close the poll now and show final results |

Votes are keyed by `SenderKey` (stable public-key hex), not name, so a re-vote moves
the single vote rather than adding another, and name collisions can't corrupt the
tally. State is in-memory (lost on restart).

## Auto-close timer

The poll auto-closes after `round_sec` seconds (default 180). On timeout the results
are rendered and the poll deleted under the lock, then pushed via the shared
`games.Announcer` (wired at startup in `cmd/bot/bot.go`) — resolving the scope to the
channel and enqueuing over the radio. A `token` guards against a stale timer fire that
races `.vote end`.

Results are multi-line; with many options they exceed 140 bytes. Add
`shrink.overrides.vote: split` to wrap results across messages instead of truncating.

## Configuration

YAML key `vote`. **Opt-in** — when `enabled: false` the command is invisible to routing
and `.help` (no "not available" reply). Zero-value ints defaulted at use sites.

```yaml
vote:
  enabled: false       # opt-in
  round_sec: 180       # auto-close countdown (default 180 = 3 min)
  max_options: 6       # max options per poll (default 6)
```

## Layout

| File | Role |
|------|------|
| `vote.go` | Plugin shell: `Meta`, `Handle`, verb dispatch |
| `poll.go` | State, logic, timer, renderer |
| `config.go` | `Configurable`: `enabled` / `round_sec` / `max_options` |
