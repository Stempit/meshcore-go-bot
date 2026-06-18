# `.num` — Guess the Number

A turn-based number-guessing game playable in **DMs and public channels**. The
bot picks a secret number; players narrow it down with `больше` / `меньше`
hints until someone hits it.

Designed for MeshCore constraints: high latency, **140 bytes per message**
(≈65 Cyrillic characters, since each Russian letter is 2 bytes in UTF-8). Every
turn is one short line and a full game is a handful of round-trips.

---

## Scope: how DM and channel share one mechanic

Game state is keyed by a **scope string**, not by channel alone:

```
scope = channelName            if channelName != ""
        "dm:" + senderName     otherwise
```

- In a **channel**, all players share one game — it becomes a race; the player
  whose guess lands gets credited by the send layer's `@[SenderName]` prefix.
- In a **DM**, each peer gets a private single-player game.

There is **no "channels only" restriction** (unlike `.dice` sessions). The same
code path serves both; only the scope key differs.

> The send layer prepends `@[SenderName]` **only on channel replies**, not DMs.
> So reply text never contains the player's name.

---

## Command surface

| Input | Action |
|-------|--------|
| `.num` / `.num start [max]` | Open a game in this scope; bot picks 1…`max` (default 100) |
| `.num <n>` | Make a guess |
| `.num status` | Show range hint and attempt count |
| `.num end` | Reveal the secret and close the game |

`max` is capped (suggested range `10`…`10000`, default `100`).

---

## State

Per scope, held in a `map[string]*numGame` guarded by a `sync.Mutex` (same shape
as `diceSessions`):

```
secret    int   // the number to guess
attempts  int   // guesses made so far
lo, hi    int   // live bounds, narrowed on each guess for the status hint
```

Tiny and fully in-memory; lost on restart, which is acceptable.

---

## Replies (all ≤ 140 bytes)

| Situation | Reply |
|-----------|-------|
| Game opened | `🎯 Загадал 1–100. .num <число>` |
| Guess too low | `больше` |
| Guess too high | `меньше` |
| Correct | `🎯 Угадал за 5! Это 42` |
| Status | `Диапазон 1–100, попыток: 3` |
| Reveal on end | `Было 42` |

On each guess, narrow `lo`/`hi` so `.num status` gives a live, useful hint over
high latency.

---

## Edge cases

| Case | Reply |
|------|-------|
| Game already open on `start` | `Игра идёт. .num <число>` |
| Guess with no game open | `Игра не начата. .num start` |
| Non-numeric guess | usage line |
| Guess outside current range | `Вне диапазона 1–100` |
| `max` out of range | clamp or usage line |

---

## Implementation notes

- New file `internal/commands/num.go`; handler signature mirrors `.dice`:
  `ProcessNumCommand(text, senderName, channelName string) ([]string, error)`.
- Strip the prefix with `strings.TrimPrefix` + `strings.TrimSpace`; parse args
  with `strings.Fields`.
- Route by sub-verb in a `switch` (like `ProcessDiceCommand`); the `default`
  branch tries to parse a bare number as a guess.
- Register one case in the `switch` in
  [internal/commands/commands_server.go](internal/commands/commands_server.go).
- Share the `scope(senderName, channelName)` helper with [HANG.md](HANG.md).
- Random source: `math/rand` as in `dice.go`.

---

## Build order

Build `.num` first — it is the smaller game and proves the shared scope-key
helper that [`.hang`](HANG.md) reuses.
