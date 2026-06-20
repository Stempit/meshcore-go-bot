# `.words` — Anagram (Слова из слова)

A timed word-building game playable in **DMs and public channels**. The bot
picks one long Russian noun (the *base word*); players race to build shorter
words from its letters before a countdown runs out. Longer words score
disproportionately more, so the game rewards digging for hard finds.

Designed for MeshCore constraints: high latency, **140 bytes per message**
(≈65 Cyrillic characters — each Russian letter is 2 bytes in UTF-8). Every reply
is a single short line; the end-of-round results may span a few messages.

> Example: base word `кальвадос` → a player sends `.words лава` →
> `✅ +16 очков` (4 letters, `4² = 16`).

---

## What's new here vs. the other games

`.num`, `.hang`, and `.dice` are pure **request/response**: a command comes in,
a reply goes out, nothing happens on its own. `.words` is the first game with a
**self-firing timer** — when the round expires the bot must *push* the results
to the channel/DM with no triggering message. That requires one new piece of
infrastructure: an **async announcer** the game can call from a timer goroutine
(see [Async results delivery](#async-results-delivery)). Everything else reuses
the existing `games` helpers.

---

## Scope: how DM and channel share one mechanic

Game state is keyed by the shared **scope string** (identical to [`.num`](NUM.md)
and [`.hang`](HANG.md)), via the existing `gameScope(senderKey, channelName)`
helper in [internal/games/game.go](../../internal/support/games/game.go):

```
scope = channelName          if channelName != ""
        "dm:" + senderKey     otherwise
```

- In a **channel** the round is **collaborative-competitive**: one shared base
  word, but each player accumulates their own score, and everyone sees each
  other's finds. The winner is the highest scorer at timeout.
- In a **DM** it is solo: one player racing their own clock.

Same code path for both; only the scope key differs.

> The send layer prepends `@[SenderName]` **only on channel replies**, not DMs.
> So per-find reply text never contains the finder's name. The timeout results
> message *does* name players, because it is pushed by the announcer outside the
> normal reply path (no `@[...]` prefix is added to it).

---

## Command surface

| Input | Action |
|-------|--------|
| `.words` / `.words start` | Pick a base word and open a round (resets timer) |
| `.words <слово>` | Submit a word built from the base word's letters |
| `.words status` | Show base word, time left, and your current score |
| `.words end` | Close the round early and show results now |
| `.words top` | Top winners in this scope over the last 30 days |

Every `.words <…>` command (start, a submission, even `status`) **resets the
countdown** to its full length (requirement 6). Only `end` and natural timeout
close the round.

---

## State

Per scope, held in a `map[string]*wordsGame` guarded by a `sync.Mutex`:

```go
type wordsGame struct {
    base     []rune          // normalized base word, e.g. []rune("кальвадос")
    avail    map[rune]int    // letter -> count available in base (multiset)
    found    map[string]bool // words already claimed (global, anti-duplicate)
    scores   map[string]int  // player name -> total score
    timer    *time.Timer     // countdown; reset on every command
    deadline time.Time       // wall-clock expiry; drives status time-left AND the stale-fire guard
    token    uint64          // round identity, guards stale timer fires across start/end
}
```

In-memory; lost on restart, which is acceptable (a round is ≤ a few minutes).

`avail` is built once at start from `base` so each submission is validated with a
cheap multiset check instead of re-scanning the base word.

`deadline` is required because `*time.Timer` exposes no "time remaining": both
`.words status` (the `⏱2:14` field) and the stale-fire guard ([Timer
lifecycle](#timer-lifecycle-the-race-that-matters)) read `time.Until(g.deadline)`
/ `time.Now().Before(g.deadline)`. Set it to `time.Now().Add(ttl)` on start and on
every reset, in lockstep with `timer.Reset`.

---

## Submission validation

A submitted word is accepted **iff all** of these hold. Run them in this order
(cheapest first); the first failure decides the reply.

1. **Normalize** with the existing `normalizeRu` helper (lowercase, `ё→е`, keep
   only `а–я`). Empty after normalization → `❌ только русские буквы`.
2. **Length ≥ `wordsMinSub`** (default 3 runes) → `❌ слишком короткое`.
3. **Not the base word itself** → `❌ это само слово`.
4. **Not already found** this round → `❌ уже было`.
5. **Buildable from the base letters** — multiset containment: count the runes of
   the submission and verify each count ≤ `avail[rune]` (one base letter can back
   at most one submission letter). Fails → `❌ слово не найдено`.
6. **Exists in the dictionary** — membership in the embedded validation set.
   Fails → `❌ слово не найдено`.

On success: `n := len([]rune(word)); score := n*n ; g.scores[player] += score`;
record the find; reply `✅ +<score> очков` (requirement 4). **Use rune length, not
`len(word)`** — `len("лава")` is 8 bytes, so `len(word)²` would score 64 instead
of 16. Steps 5 and 6 share the generic
`слово не найдено` reply (requirement 5); the earlier, more specific messages are
a UX nicety and may be collapsed to the same string if preferred.

> **Multiset, not set.** `кальвадос` has two `а` but only one `с`; `скос` needs
> two `с` and is therefore **rejected** even though every distinct letter is
> present.

### Scoring

`score = n * n` where `n = len([]rune(word))` (super-linear, per the design
decision) — long words pay off disproportionately, which "stimulates finding long
and hard words" (requirement 4):

| len | 3 | 4 | 5 | 6 | 7 |
|-----|---|---|---|---|---|
| pts | 9 | 16 | 25 | 36 | 49 |

The base coefficient is fixed in code as the square; no per-find config knob is
needed. (If tuning is ever wanted, multiply by a `Words.coef` config field.)

---

## Replies (per-find, all ≤ 140 bytes)

| Situation | Reply |
|-----------|-------|
| Round opened | `🔤 Слова из: КАЛЬВАДОС — 5 мин!` |
| Valid find | `✅ +16 очков` |
| Not a word / not buildable | `❌ слово не найдено` |
| Too short | `❌ слишком короткое` |
| Already claimed | `❌ уже было` |
| Is the base word | `❌ это само слово` |
| Non-Cyrillic input | `❌ только русские буквы` |
| `status` | `КАЛЬВАДОС ⏱2:14 ты:41` |
| Submit with no round | `Игра не начата. .words start` |
| Round already running (`start`) | `Игра идёт. .words <слово>` |
| Dictionary not loaded | `Словарь недоступен` |
| Leaderboard (`top`) | `🏆 Аня 5, Боб 3` |
| Leaderboard, empty | `Пока никто не выигрывал` |

Add a byte-length guard on every reply (reuse the `.llm` shrink/limit logic) so a
long base word can never overflow 140 bytes — the round-opened line in particular
must fit `🔤 Слова из: <BASE> — N мин!` for the longest allowed base word.

---

## End of round (results)

Triggered two ways, both routed through one `finishRound(scope)` function:

1. **Timeout** — the `time.Timer` fires (requirement 7). The game finalizes
   itself and **pushes** the results via the announcer.
2. **`.words end`** — same finalize, but the results are returned synchronously as
   the command reply (no announcer needed for this path).

`finishRound` ranks players by total score (descending; tie-break by name),
renders the results, logs the winner to `historylog`, and deletes the game.

Results layout — **header + ranked players with scores only** (no per-word
breakdown), as two ≤140-byte lines:

```
🏁 Итоги «КАЛЬВАДОС»
🏆 Аня 85 · Боб 41 · Вася 16
```

- Line 1: header with the base word.
- Line 2: players ranked by total score (descending; tie-break by name), capped
  at `gameTopMax` (5) players, **built to a byte budget the same way
  [`gameTop`](../../internal/support/games/game.go) builds its line** (append players
  while they fit, then stop). Do **not** rely on `shrink.Fit` to *split* this
  line: with no `shrink` override configured, `Fit` resolves to the **Truncate**
  variant, which would silently *cut* trailing players rather than wrap them. If
  wrapping is genuinely wanted, add the config override `shrink.overrides.words:
  split` — that is a config change, not code in `internal/shrink`.
- No finds at all → `🏁 Никто не успел. Было: КАЛЬВАДОС`.

The highest scorer is recorded as the **winner** for `.words top`. Ties record
all top-scoring players (matching `.dice`'s tie handling).

---

## Async results delivery

This is the one genuinely new mechanism. The game runs a `time.AfterFunc`; when
it fires there is no incoming message and therefore no `Outgoing` target in
scope. We give the `games` package an injected callback that turns a **scope
string back into a channel/contact** and enqueues.

**1. games package — an injectable announcer (decoupled, no meshcore import):**

```go
// internal/games/game.go
// Announcer pushes unsolicited game messages (e.g. round results) to a scope.
type Announcer func(scope string, msgs []string)

var announce Announcer = func(string, []string) {} // no-op until wired

// SetAnnouncer is called once at startup from the composition root.
func SetAnnouncer(a Announcer) { if a != nil { announce = a } }
```

The timer callback calls `announce(scope, results)` while **not** holding
`wordsMu` (resolve + enqueue can be slow; never hold the game lock across it).

**2. dispatch — resolve scope → target, reuse the existing send paths:**

```go
// internal/dispatch/dispatch.go
// AnnounceToScope resolves a games scope string to a channel or contact and
// queues each message. Mirrors SendChannelMessage / SendDirectMessage.
func (d *Dispatcher) AnnounceToScope(scope string, msgs []string) {
    if rest, ok := strings.CutPrefix(scope, "dm:"); ok {
        key := []byte(rest)                       // senderKey is raw pubkey bytes for DMs
        c := d.client.FindContactByKeyPrefix(key[:min(6, len(key))])
        if c == nil { log().Warn("announce: contact not found"); return }
        for _, m := range shrink.Fit("words", "", msgs) {
            d.SendDirectMessage(c, m, true)
        }
        return
    }
    ch := d.client.GetChannelByName(scope)         // channel scope == channel name
    if ch == nil { log().Warn("announce: channel not found", "scope", scope); return }
    for _, m := range shrink.Fit("words", "", msgs) {
        d.SendChannelMessage(ch, m, false)
    }
}
```

> **Delivery is best-effort.** `SendChannelMessage` / `SendDirectMessage` use
> `TryEnqueue` ([dispatch.go](../../internal/dispatch/dispatch.go)), which drops
> the message if the outgoing queue is full rather than blocking the timer
> goroutine. A full queue is rare, but it means a timeout's results can be lost
> (it logs a warning). Acceptable for a game — don't add blocking sends in the
> timer callback to "fix" it.

> **DM scope detail.** In `processDM`, `senderKey` is `string(contact.PublicKey)`
> — *raw bytes*, not hex (see [process.go](../../internal/dispatch/process.go)).
> So `"dm:" + senderKey` carries the raw pubkey; `[]byte(rest)` recovers it for
> `FindContactByKeyPrefix`. (Channel scope is just the channel name.) Verify this
> against the live `gameScope` before coding — it's the easiest thing to get
> wrong.

**3. composition root — wire it after the dispatcher exists:**

```go
// cmd/bot/bot.go, near the greeter/migrator wiring
games.SetAnnouncer(d.AnnounceToScope)
```

No import cycle: `games` exposes a func type and never imports `dispatch` or
`meshcore`; `dispatch` already imports neither `games` nor `commands` for this
(the callback is injected, not called by name).

---

## Timer lifecycle (the race that matters)

One timer per round, reset on every command, fired exactly once on timeout:

- **Start:** set `g.deadline = time.Now().Add(ttl)` and
  `g.timer = time.AfterFunc(ttl, func() { onTimeout(scope, token) })`. `token` is
  the round's identity (a monotonic counter or the `*wordsGame` pointer captured
  by value); it changes only across `start`/`end`, **never on a reset**.
- **Reset (each command):** under `wordsMu`, set `g.deadline = time.Now().Add(ttl)`
  then `g.timer.Reset(ttl)`. Because we never drain the timer channel (`AfterFunc`
  has none), `Reset` is safe here.
- **Timeout fire:** the callback acquires `wordsMu`, looks up the game, and
  **bails if any** of these hold:
  1. the round is gone or its `token` no longer matches — it was ended
     (`.words end`) or restarted between the fire and the lock; or
  2. `time.Now().Before(g.deadline)` — a command `Reset` the timer in the window
     between this fire and the lock, so this is a *stale* fire and the rescheduled
     timer will fire again at the new deadline; just `return`. **The `token` does
     not change on a reset, so without this deadline check a reset that races the
     fire would lose and the round would end early.**
  Otherwise it builds the results, deletes the game, **releases the lock**, then
  calls `announce(scope, results)`.
- **`.words end`:** stop the timer (`g.timer.Stop()`), finalize synchronously,
  return the results as the command reply. If the game is **already gone** (a
  timeout finalized it microseconds earlier), treat it as "no active round" and
  reply accordingly rather than erroring or double-finalizing.

Together the `token` guard (covers `end`/restart) and the `deadline` guard (covers
a reset racing the fire) make every stale-fire case harmless.

---

## Word bank (runtime file)

The dictionary is a **runtime file loaded at startup**, not compiled in — same
class as `config.yaml` and the `log/news/` files. **The file already exists:**

```
words_anagram_ru.txt   // in CWD, 66 927 lowercase nouns, ё-folded, LF, sorted, ~1.3 MB
```

It was produced from the user-provided `singular.txt` (67 774 raw entries) by:
lowercasing, folding `ё→е`, keeping only pure-Cyrillic single tokens (dropping
710 hyphenated + 3 spaced multi-word entries), dropping anything shorter than 3
runes, and de-duplicating. The result is **66 927 words**, of which **25 609**
fall in the 8–10-rune base-word band.

The path is configurable (`Words.DictPath`, default `words_anagram_ru.txt`). The
loader is **defensive**: it runs every line through `normalizeRu` and re-applies
the ≥3-rune / pure-Cyrillic filter, so pointing `DictPath` straight at a raw
`singular.txt` also works (just slower to load). Loaded **once at startup** (not
per-round) into two in-memory indexes held in the `games` package:

- **Validation set** — `map[string]struct{}` of every accepted entry, used for
  membership in submission step 6.
- **Base-word pool** — `[]string` of entries with `BaseMinLen..BaseMaxLen` runes
  (default **8–10**, ~25.6k words). `start` picks uniformly from this pool.

Expose a `games.LoadWordsDict(path string) error` called from the composition
root (after config is read). On a missing/empty file, log a warning and leave the
indexes empty; `.words start` then replies `Словарь недоступен` instead of
crashing — the rest of the bot runs normally.

> The existing `words_ru.txt` (60 words) stays embedded as the **hangman** bank;
> `.words` loads its own, much larger `words_anagram_ru.txt` at runtime, so the
> two games are sourced and tuned independently.

---

## Config

Add a `Words` block to [internal/config/model.go](../../internal/config/model.go)
(YAML tag required — check for typos) and document it in `config.example.yaml`:

```go
Words struct {
    Enabled    bool   `yaml:"enabled"`      // opt-in: .words stays off unless this is true
    DictPath   string `yaml:"dict_path"`    // dictionary file, default "words_anagram_ru.txt"
    RoundSec   int    `yaml:"round_sec"`    // countdown length, default 300 (5 min)
    MinSubLen  int    `yaml:"min_sub_len"`  // shortest valid submission, default 3
    BaseMinLen int    `yaml:"base_min_len"` // shortest base word, default 8
    BaseMaxLen int    `yaml:"base_max_len"` // longest base word, default 10
} `yaml:"words"`
```

Access via `config.Get().Words` (read-only). There is no central config defaulter
(see [config.go](../../internal/config/config.go)), so apply each `int` default at
the use site when the value is zero — exactly like `shrink.maxBytes()` and the
`ChannelTransmissions < 1` fallback in dispatch.

`.words` is **opt-in** (same convention as `Claw.Enabled`): it stays off until
`words.enabled: true`. This is the only representable default here — a zero `bool`
is `false` and nothing flips it back, so a `bool` defaulting to *true* is not
achievable without a pointer or a defaulter. Gate the command on
`config.Get().Words.Enabled` in `GenerateReplies` (see the dispatch checklist
item) so an unconfigured bot ignores `.words` entirely.

---

## Logging

**1. Operational log** — `log/slog`, English, structured (project convention):

```go
slog.Info("words round started", "scope", scope, "base", string(g.base))
slog.Info("words find", "scope", scope, "player", who, "word", w, "score", s)
slog.Info("words round ended", "scope", scope, "winner", who, "score", top)
```

**2. Results log** — reuses the **`historylog`** package, no new package, exactly
like `.num`/`.hang`. Add a package-level logger:

```go
wordsLogBase = filepath.Join(gamesBaseDir, "words")
wordsLog     = historylog.New(wordsLogBase)
```

Record the **winner** of each round with
`logWin(wordsLog, scope, winner, detail)` where `detail` is the winning total
(or the base word). Files land under `log/games/words/channels/<scope>.txt`, with
DM scopes written as `dm:<sender>` (same `sanitize` behavior the other games rely
on). Rounds with no finds are not recorded.

---

## `.words top` — leaderboard

Identical mechanism to `.num top` / `.hang top`: reuse the shared
`gameTop(wordsLogBase, scope)` helper — last 30 days, wins per player, ranked
descending, capped at 5 players, one ≤140-byte line.

- Works in channels and DMs; most meaningful in channels.
- Empty/missing log → `Пока никто не выигрывал`.

---

## Implementation checklist

- [ ] `internal/games/words.go` — `ProcessWordsCommand(text, senderKey, senderName, channelName string) ([]string, error)`, `LoadWordsDict(path)`, the `wordsGame` state, validation, scoring, timer, `finishRound`.
- [x] `words_anagram_ru.txt` (CWD) — runtime dictionary, 66 927 words, already generated from `singular.txt`.
- [ ] `internal/games/game.go` — add `Announcer` type, `announce` var, `SetAnnouncer`, and `wordsLog`/`wordsLogBase`.
- [ ] `internal/dispatch/dispatch.go` — `AnnounceToScope`.
- [ ] `cmd/bot/bot.go` — `games.LoadWordsDict(config.Get().Words.DictPath)` at startup, and `games.SetAnnouncer(d.AnnounceToScope)` after the dispatcher is constructed.
- [ ] `internal/commands/commands_server.go` — add `case lower == ".words", strings.HasPrefix(lower, ".words "):` that first bails when `!config.Get().Words.Enabled` (`return nil, "", nil`), otherwise returns `replies, "words", err`.
- [ ] `internal/config/model.go` + `config.example.yaml` — the `Words` block.
- [ ] `internal/commands/help.go` — `wordsHelp` constant, add `.words` to `helpList`, add the `case "words"`.
- [ ] `CLAUDE.md` — add `.words` to the commands table and link this doc.
- [ ] (optional) `config.example.yaml` — add `shrink.overrides.words: split` *only* if you want overlong result lines to wrap instead of truncate; otherwise build each line to a byte budget (like `gameTop`) and leave shrink alone. This is config, not code in `internal/shrink`.
- [ ] `go vet ./...` passes; manual test via `cmd/example`.

---

## Build order

Build the synchronous core first — `start`, submission validation, scoring,
`status`, `end` (which returns results inline). Add the **async announcer +
timeout path last**, once the resolve-scope-to-target logic is verified against a
real DM and a real channel. That isolates the one new piece of infrastructure
from the game logic, which is otherwise a straightforward sibling of `.hang`.
