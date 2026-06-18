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

Add a byte-length guard on every reply (reuse the `.llm` shrink/limit logic) so
a long word-bank entry can never overflow 140 bytes.

---

## Word bank

Embed the list into the `commands` package with `//go:embed`:

```
internal/commands/words_ru.txt   // nouns, 4–8 letters, lowercased, ё-stripped
```

Prefer embedding over a runtime file: the `log/news/` files come from the
external [newswire](https://github.com/Stempit/newswire) app and may be absent,
whereas an embedded list is always present and needs no deploy step. Load and
filter once at init.

---

## Implementation notes

- New file `internal/commands/hangman.go`; handler signature mirrors `.dice`:
  `ProcessHangCommand(text, senderName, channelName string) ([]string, error)`.
- Strip the prefix with `strings.TrimPrefix` + `strings.TrimSpace`; route
  sub-verbs in a `switch`. The `default` branch decides between a single-letter
  guess and a full-word guess by rune count.
- Iterate the word with `[]rune`, never bytes — Cyrillic is multibyte.
- Register one case in the `switch` in
  [internal/commands/commands_server.go](internal/commands/commands_server.go).
- Share the `scope(senderName, channelName)` helper with [NUM.md](NUM.md).

---

## Build order

Build [`.num`](NUM.md) first; it establishes the shared scope-key helper that
`.hang` reuses.
