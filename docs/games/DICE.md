# `.dice` — Roll Dice & Channel Roll-off

Two modes in one command:

1. **Plain roll** — roll `NdM` immediately and see the result. Works in **DMs
   and public channels**.
2. **Roll-off** — a multi-player round in a channel: someone opens it, players
   roll once each, the highest total wins. **Channels only.**

Designed for MeshCore constraints: high latency, **140 bytes per message**
(≈65 Cyrillic characters, since each Russian letter is 2 bytes in UTF-8). Every
reply is one short line.

Source: [internal/commands/dice.go](../../internal/commands/dice.go). Handler
signature `ProcessDiceCommand(text, senderName, channelName string) ([]string, error)`,
wired into the `switch` in
[internal/commands/commands_server.go](../../internal/commands/commands_server.go).

---

## Command surface

| Input | Action | Where |
|-------|--------|-------|
| `.dice` | Roll `1d6` | DM + channel |
| `.dice NdM` | Roll N dice of M faces (e.g. `.dice 2d6`) | DM + channel |
| `.dice M` | Shorthand for `1dM` (e.g. `.dice 20` = `1d20`) | DM + channel |
| `.dice start [NdM]` | Open a roll-off round (default `1d6`) | channel only |
| `.dice roll` | Roll into the open round | channel only |
| `.dice end` | Close the round, announce the winner | channel only |
| `.dice status` | Show current standings | channel only |

Routing: `ProcessDiceCommand` strips the `.dice` prefix, takes the first field
as the sub-verb, and `switch`es on it. The `default` branch treats the rest as a
dice spec and does a plain roll, so `.dice`, `.dice 2d6`, and `.dice 20` all fall
through to `dicePlainRoll`.

---

## Dice notation (`parseDiceSpec`)

Accepts `NdM`, `dM`, a bare `M`, or empty:

- `""` → default `1d6`.
- `"NdM"` → split on the first `d`; empty left side defaults N to 1 (`d20` = `1d20`).
- `"M"` (no `d`) → single die with M faces.

Caps, enforced after parsing:

| Const | Value | Meaning |
|-------|-------|---------|
| `diceMaxCount` | 10 | max dice in one roll |
| `diceMinSides` | 2 | a die needs ≥ 2 faces |
| `diceMaxSides` | 1000 | upper bound on faces |
| `diceDefaultN` | 1 | dice count when none given |
| `diceDefSides` | 6 | faces when none given (a d6) |

Anything unparseable or out of range returns the usage line instead of an
error (`diceUsage`):

```
🎲 .dice [NdM] | .dice start [NdM] / roll / end
```

Rolling (`rollDice`) seeds `math/rand` from `time.Now().UnixNano()` — same
pattern as `eightball.go` — and returns both the individual results and their
sum.

---

## Roll-off state

Per channel, held in a `map[string]*diceSession` guarded by a `sync.Mutex`:

```go
type diceSession struct {
    notation string         // human form, e.g. "2d6"
    n, sides int            // parsed dice count and faces
    rolls    map[string]int // playerName -> total
}

var (
    diceSessions = map[string]*diceSession{} // keyed by channel name
    diceMu       sync.Mutex
)
```

Tiny and fully in-memory; lost on restart, which is acceptable.

Design choices that matter under mesh latency:

- **Keyed by player name, not arrival order.** Rolls go into a map keyed by
  `senderName`, so out-of-order delivery doesn't affect the outcome.
- **One roll per player.** A second `.dice roll` from the same name is rejected
  with their existing total, so a **re-delivered/duplicate** message can't
  change the result.
- **No timeout.** A round stays open until someone runs `.dice end`. There is
  deliberately no auto-expiry — handlers are request→response and have no handle
  to the send layer, so they cannot push an unsolicited "round over"
  message. (An earlier lazy-TTL version was removed for being surprising: it
  only fired on the *next* `.dice` command and silently discarded the round.)
  Trade-off: a round nobody ends lingers in the map until restart, and
  `.dice start` reports "уже идёт" until it's closed.

---

## Replies (all ≤ 140 bytes)

The send layer prepends `@[SenderName]` **only on channel replies**, not DMs.
Self-attributed rolls therefore omit the name to avoid showing it twice — e.g.
the user sees `@[Stempit] 🎲 : 11`.

| Situation | Reply |
|-----------|-------|
| Plain roll, 1 die | `🎲 : 11` |
| Plain roll, N dice | `🎲 : 4+5 = 9` |
| Round opened | `🎲 Раунд открыт: 2d6. Бросай: .dice roll` |
| Roll into round | `🎲 : 11` |
| Winner on end | `🏆 Bob: 11 (Alice 9)` |
| Tie on end | `🤝 Ничья: Bob, Carol — 11` |

Standings (`diceResult`) sort by total descending, name ascending as a
tiebreak. Everyone sharing the top total is named in the head; the next places
are appended in parentheses, **capped at `diceWinnersMax` (4)** so the line
survives the mesh size limit.

---

## Edge cases

| Case | Reply |
|------|-------|
| Session verb in a DM | `Раунд доступен только в каналах` |
| `.dice start` with a round already open | `Раунд 2d6 уже идёт. .dice roll` |
| `.dice roll` / `end` / `status` with no round | `Раунд не открыт. .dice start` |
| Same player rolls twice | `Ты уже бросил: 9` |
| `.dice end` with zero rolls | `Раунд закрыт, никто не бросил` |
| `.dice status` with zero rolls | `Раунд 2d6: бросков пока нет` |
| Empty / unparseable / out-of-range spec | usage line |
| Empty sender name | falls back to `?` (`diceWho`) |

---

## Help

`.dice` is listed in `helpList` and has a detail entry in
[internal/commands/help.go](../../internal/commands/help.go):

```
.dice [NdM] — кинуть кости (def 1d6)
.dice start [NdM] / roll / end — раунд в канале
```
