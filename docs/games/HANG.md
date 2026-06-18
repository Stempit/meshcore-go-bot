# `.hang` — Hangman (Виселица)

A letter-guessing word game playable in **DMs and public channels**. The bot
picks a hidden Russian word; players reveal it one letter at a time before the
lives run out.

Designed for MeshCore constraints: high latency, **140 bytes per message**
(≈65 Cyrillic characters, since each Russian letter is 2 bytes in UTF-8). The
masked word, used letters, and lives must all fit in one line — which is why
words are kept short.

---

## Scope: how DM and channel share one mechanic

Game state is keyed by a **scope string**, not by channel alone (identical to
[`.num`](NUM.md)):

```
scope = channelName            if channelName != ""
        "dm:" + senderName     otherwise
```

- In a **channel** the game is naturally **collaborative**: shared word, shared
  lives, everyone contributes letters. The player who reveals the last letter is
  credited by the send layer's `@[SenderName]` prefix.
- In a **DM** it is solo.

Same code path for both; only the scope key differs.

> The send layer prepends `@[SenderName]` **only on channel replies**, not DMs.
> So reply text never contains the player's name.

---

## Command surface

| Input | Action |
|-------|--------|
| `.hang` / `.hang start` | Pick a random word and open a game |
| `.hang <буква>` | Guess one Cyrillic letter |
| `.hang <слово>` | Guess the whole word (optional convenience) |
| `.hang status` | Show current mask, used letters, lives |
| `.hang end` | Give up and reveal the word |
| `.hang top` | Top winners in this scope over the last 30 days |

---

## State

Per scope, held in a `map[string]*hangGame` guarded by a `sync.Mutex`:

```
word      string         // the target word (normalized, lowercase)
guessed   map[rune]bool  // letters that have been tried
wrong     int            // wrong guesses so far
lives     int            // starts at 6
```

In-memory; lost on restart, which is acceptable.

---

## Russian-specific rules (these matter)

- **Normalize `ё` → `е`** on both the word and the guess, so players never need
  to type `ё`.
- **Lowercase and case-insensitive.**
- **Reject non-Cyrillic input** → `Только русские буквы`.
- A **duplicate letter** does not cost a life → `Буква 'а' уже была`.
- **Keep words ≤ 8 letters** so `mask + used letters + lives` stays under the
  140-byte ceiling.
- Render the mask with **no spaces** (`к__к_`) to save bytes.

---

## Replies (all ≤ 140 bytes)

| Situation | Reply |
|-----------|-------|
| Game opened | `к__к_  ❤6` |
| Correct letter | `к__ка  ❤6` |
| Wrong letter | `к__к_  ✗б  ❤5` |
| Status | `к__к_  ✗б,о  ❤4` |
| Win (all revealed) | `🎉 КОШКА` |
| Lose (lives = 0) | `💀 Было: КОШКА` |
| Give up (`end`) | `💀 Было: КОШКА` |
| Duplicate letter | `Буква 'а' уже была` |
| Non-Cyrillic input | `Только русские буквы` |
| Guess with no game | `Игра не начата. .hang start` |
| Leaderboard (`top`) | `🏆 Аня 5, Боб 3, Вася 1` |
| Leaderboard, empty | `Пока никто не выигрывал` |

Add a byte-length guard on every reply (reuse the `.llm` shrink/limit logic) so
a long word-bank entry can never overflow 140 bytes.

---

## Logging

Two distinct kinds of logging:

**1. Operational log** — `log/slog`, English, structured key-value pairs (per
the project convention). Trace game lifecycle for debugging:

```
slog.Info("hang game started", "scope", scope, "len", runeCount)
slog.Info("hang win", "scope", scope, "winner", who, "word", word)
slog.Info("hang loss", "scope", scope, "word", word)
```

**2. Results log** — an append-only file that feeds `.hang top`. **Reuses the
existing `historylog` package, no new package.** Point a second
`historylog.Logger` at a games base dir (e.g. `<historyBase>/games/hang`) and
record each **won** game with `LogChannelIncoming(scope, winner, word, now)`,
which writes the standard `historylog` line:

```
<< 2026-06-19 14:03:22 [Аня]: кошка
```

`Sender` carries the winner (whoever revealed the last letter), `Text` carries
the solved word. Losses / give-ups are not recorded.

**Scope → file name.** Both DM and channel results go through `historylog`'s
*channel* reader/writer, using the scope string as the name:

```
log/games/hang/channels/<channel>.txt        – channel: name = channel
log/games/hang/channels/dm:<sender>.txt       – DM: name = "dm:" + sender
```

`sanitize` makes `dm:<sender>` a safe filename and the prefix avoids collisions
with a real channel. No `dm/` namespace, so `historylog` needs no changes.

---

## `.hang top` — leaderboard

Reads the current scope's results log, keeps entries from the **last 30 days**,
counts wins per player, ranks descending (tie-break by name), and caps the list
at **5 players** (mesh size, like `diceWinnersMax`).

Read with `historylog.ReadAllChannel(<gamesBase>, scope)` → `[]Entry`, then do
the 30-day filter / count / rank in the handler.

- Works in both channels and DMs; most meaningful in channels.
- Empty or missing log → `Пока никто не выигрывал`.
- Whole line must stay ≤ 140 bytes; truncate the tail if needed.

In a channel the game is collaborative, so the credited winner is whoever landed
the final letter — same as the `@[SenderName]` the send layer prepends.

---

## Word bank

Embed the list into the `games` package with `//go:embed`:

```
internal/games/words_ru.txt   // nouns, 4–8 letters, lowercased, ё-stripped
```

Prefer embedding over a runtime file: the `log/news/` files come from the
external [newswire](https://github.com/Stempit/newswire) app and may be absent,
whereas an embedded list is always present and needs no deploy step. Load and
filter once at init.

---

## Implementation notes

- New file `internal/games/hangman.go` (package `games`); handler signature mirrors `.dice`:
  `ProcessHangCommand(text, senderName, channelName string) ([]string, error)`.
- Strip the prefix with `strings.TrimPrefix` + `strings.TrimSpace`; route
  sub-verbs in a `switch`. The `default` branch decides between a single-letter
  guess and a full-word guess by rune count.
- Iterate the word with `[]rune`, never bytes — Cyrillic is multibyte.
- Register one case in the `switch` in
  [internal/commands/commands_server.go](internal/commands/commands_server.go).
- Share the `scope(senderName, channelName)` helper with [NUM.md](NUM.md).
- Results logging reuses **`historylog`** (no new package): the same
  package-level `*historylog.Logger` as `.num`, pointed at the `hang/` games
  base dir, with `LogChannelIncoming` for writes and `ReadAllChannel` for `top`.
  Base dir resolved from `config.Get()` (read-only).

---

## Build order

Build [`.num`](NUM.md) first; it establishes the shared scope-key helper that
`.hang` reuses.
