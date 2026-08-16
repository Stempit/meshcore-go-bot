# num — guess the number

Package `num` registers the `.num` command: a turn-based number-guessing game
playable in **DMs and channels**. The bot picks a secret number; players narrow it
down with `больше` / `меньше` hints until someone hits it.

Every turn is one short line, sized for the MeshCore 140-byte-per-message limit.

## Scope: DM and channel share one mechanic

Game state is keyed by a **scope string** (via the shared `games.Scope` helper), not
by channel alone:

```
scope = channelName          if channelName != ""
        "dm:" + senderKey     otherwise
```

- In a **channel**, all players share one game — it becomes a race; the winner is credited by the send layer's `@[Sender]` prefix.
- In a **DM**, each peer gets a private single-player game.

There is no "channels only" restriction; only the scope key differs.

## Command surface

| Input | Action |
|-------|--------|
| `.num` / `.num start [max]` | Open a game; bot picks 1…`max` (default 100) |
| `.num <n>` | Make a guess |
| `.num status` | Show range hint and attempt count |
| `.num end` | Reveal the secret and close the game |
| `.num top` | Top winners in this scope over the last 30 days |

On each guess the live `lo`/`hi` bounds narrow so `.num status` gives a useful hint
over high latency. State is in-memory (lost on restart).

## Leaderboard (`.num top`)

Wins are recorded through the shared `games` support layer, which appends each won
game to `log/games/num/channels/<scope>.txt` (DM scopes stored as `dm:<sender>`).
`.num top` reads that log, keeps the last 30 days, counts wins per player, ranks
descending, and caps at 5 players. Empty/missing log → `Пока никто не выигрывал`.

## Configuration

None. `.num` is always enabled.

See also the shared [`games`](../../support/games/README.md) support layer.
