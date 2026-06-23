# `.vote` — Group Poll

A per-channel poll: one sender opens a question with a list of options, everyone
casts one anonymous, changeable vote, and results are posted when the timer fires
or anyone runs `.vote end`.

Designed for MeshCore constraints: **140 bytes per message**
(≈65 Cyrillic characters). The start message lists all options; status and
results are multi-line and may be split by the send layer's `shrink.Fit`.

---

## Scope

**Channels only.** Any session verb in a DM replies
`"Голосование доступно только в каналах"`. This matches the `.dice` session
convention.

State is keyed by channel name (one active poll per channel at a time).

---

## Command surface

| Input | Action |
|-------|--------|
| `.vote start вопрос \| вариант1 \| вариант2 [...]` | Open a poll; pipe-separated first segment = question, rest = options (2–`max_options`) |
| `.vote <номер>` | Cast or change this sender's single vote to option *n* (1-based) |
| `.vote status` | Show time left and current tally |
| `.vote end` | Close the poll now and show final results |
| `.vote help` | Show `HelpLong` (intercepted centrally in `registry.Dispatch`) |

---

## State

Per channel, held in `map[string]*poll` guarded by `sync.Mutex`:

```
question  string
options   []string       // labels; index 0 = option 1
votes     map[string]int // senderKey → chosen option index (one, changeable)
deadline  time.Time      // wall-clock expiry (for status countdown + stale-fire guard)
timer     *time.Timer    // auto-close AfterFunc
token     uint64         // round identity; rejects stale timer fires
```

Votes are keyed by `SenderKey` (the sender's stable public-key hex), not by name,
so a re-vote moves the single vote rather than adding another, and name collisions
cannot corrupt the tally.

---

## Replies

| Situation | Reply |
|-----------|-------|
| Poll opened | `📊 Опрос: <вопрос>\n.vote 1 - <вариант>\n.vote 2 - <вариант>` |
| Vote cast / changed | `✅ Голос за: <вариант>` |
| Status | `⏱2:47\n📊 Итоги <вопрос>:\n1) <вариант>: 2\n2) <вариант>: 0` |
| Results (end / timeout) | `📊 Итоги <вопрос>:\n1) <вариант>: 2\n2) <вариант>: 0` |
| Poll already open | `Опрос уже идёт. .vote status` |
| No active poll | `Опрос не идёт. .vote start` |
| Bad option number | `Вариант 1–<n>` |
| Too many options | `Максимум <n> вариантов` |
| Bad input / no args | usage line |

The results block is multi-line. With many options it exceeds the 140-byte mesh
limit; add `Shrink.overrides.vote: split` to `config.yaml` to wrap it across
messages instead of truncating.

---

## Auto-close timer

The poll auto-closes after `vote.round_sec` seconds (default 180). On timeout,
`voteOnTimeout` runs:

1. Acquire `pollMu`.
2. Bail if the poll is gone or the token mismatches (stale fire after `.vote end`
   raced the timer).
3. Render results and delete the poll under the lock.
4. **Release `pollMu`**, then call `games.Announce(scope, results)`.

`games.Announce` resolves the scope to the channel and enqueues messages over the radio
— this must not run while holding `pollMu`. The same pattern is used by `.words`.

The announcer is wired at startup in `cmd/bot/bot.go`:
```go
games.SetAnnouncer(d.AnnounceToScope)
```

No change to the composition root is needed for `.vote`.

---

## Configuration

YAML key `vote` in `config.yaml`:

```yaml
vote:
  enabled: false       # opt-in; stays off until true
  round_sec: 180       # auto-close countdown (default 180 = 3 min)
  max_options: 6       # max options per poll (default 6)
```

`Enabled() bool` controls visibility in routing and `.help`. When `false`, the
command is invisible — no "not available" reply is returned.

---

## Edge cases

| Case | Behaviour |
|------|-----------|
| `.vote start` while a poll is running | Rejected: `Опрос уже идёт. .vote status` |
| `.vote end` with no poll | `Опрос не идёт. .vote start` |
| Timer fires after `.vote end` closed the poll | Token mismatch → timeout silently discarded |
| Sender re-votes | Previous vote replaced; tally updated immediately |
| No votes cast when poll closes | Results show all options at 0 |
| Sender key is empty | Vote recorded under `""` key (stable within a session) |

---

## Implementation

| File | Role |
|------|------|
| `internal/commands/vote/vote.go` | Plugin shell: `Meta`, `Handle`, `processVote` verb dispatch |
| `internal/commands/vote/poll.go` | State, logic, timer, renderer |
| `internal/commands/vote/config.go` | `Configurable`: `enabled` / `round_sec` / `max_options` |
