# dice — roll dice & channel roll-off

Package `dice` registers the `.dice` command. Two modes in one command:

1. **Plain roll** — roll `NdM` immediately and see the result. Works in **DMs and channels**.
2. **Roll-off** — a multi-player round in a channel: someone opens it, players roll once each, the highest total wins. **Channels only.**

Every reply is one short line, sized for the MeshCore 140-byte-per-message limit
(≈65 Cyrillic characters — each Russian letter is 2 bytes in UTF-8).

## Command surface

| Input | Action | Where |
|-------|--------|-------|
| `.dice` | Roll `1d6` | DM + channel |
| `.dice NdM` | Roll N dice of M faces (e.g. `.dice 2d6`) | DM + channel |
| `.dice M` | Shorthand for `1dM` (e.g. `.dice 20` = `1d20`) | DM + channel |
| `.dice start [NdM]` | Open a roll-off round (default `1d6`) | channel only |
| `.dice roll` | Roll into the open round | channel only |
| `.dice status` | Show current standings | channel only |
| `.dice end` | Close the round, announce the winner | channel only |

The verb after `.dice` is dispatched in a `switch`; the `default` branch treats the
rest as a dice spec, so `.dice`, `.dice 2d6`, and `.dice 20` all fall through to a
plain roll.

### Dice notation

Accepts `NdM`, `dM`, a bare `M`, or empty. Caps enforced after parsing: max 10 dice,
2–1000 faces; defaults `1d6`. Anything unparseable or out of range returns the usage
line (`🎲 .dice [NdM] | .dice start [NdM] / roll / end`).

## Roll-off design (mesh-latency-safe)

State is a per-channel in-memory `map[string]*diceSession` under a mutex (lost on
restart, which is acceptable).

- **Keyed by player name, not arrival order** — out-of-order delivery doesn't change the outcome.
- **One roll per player** — a re-delivered/duplicate `.dice roll` can't change the result (replies with the existing total).
- **No timeout** — a round stays open until someone runs `.dice end` (handlers are request→response and can't push an unsolicited "round over"). A round nobody ends lingers until restart.

Standings sort by total desc, name asc; everyone sharing the top total is named, next
places appended in parentheses, capped at 4 so the line survives the byte limit.

## Configuration

None. `.dice` is always enabled.
