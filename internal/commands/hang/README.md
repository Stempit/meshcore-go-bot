# hang — hangman (Виселица)

Package `hang` registers the `.hang` command: a letter-guessing word game playable in
**DMs and channels**. The bot picks a hidden word from an embedded word bank; players
reveal it one letter at a time before the 6 lives run out. The word bank follows the
bot's global `Language` setting: `words_ru.txt` for `ru`, `words_en.txt` otherwise.

The masked word, used letters, and lives must all fit in one 140-byte mesh message,
which is why the bank is kept to short words (4–8 letters).

## Scope: DM and channel share one mechanic

Same shared scope keying as [`.num`](../num/README.md): channel name, or
`"dm:" + senderKey` for DMs.

- In a **channel** the game is **collaborative**: shared word, shared lives; the player who reveals the last letter is credited by the send layer's `@[Sender]` prefix.
- In a **DM** it is solo.

## Command surface

| Input | Action |
|-------|--------|
| `.hang` / `.hang start` | Pick a random word and open a game |
| `.hang <буква>` / `<letter>` | Guess one letter |
| `.hang <слово>` / `<word>` | Guess the whole word |
| `.hang status` | Show current mask, used letters, lives |
| `.hang end` | Give up and reveal the word |
| `.hang top` | Top winners in this scope over the last 30 days |

## Normalization rules

- `Language: ru` — lowercase, **`ё` → `е`** folding on both word and guess, keeps only
  `а–я`; non-Cyrillic input → `Только русские буквы`.
- `Language: en` (default) — lowercase, keeps only `a–z`; non-letter input →
  `Letters only`.
- A duplicate letter does not cost a life.
- Mask rendered with no spaces (`к__к_`) to save bytes.

State is in-memory (lost on restart). Both word banks are embedded with `go:embed`;
`Configure` picks one at startup based on `config.Get().Language` (a restart is
required to switch, same as the rest of the bot's i18n).

## Leaderboard (`.hang top`)

Wins are recorded through the shared `games` support layer under
`log/games/hang/channels/<scope>.txt`; `.hang top` shows wins per player over the last
30 days, ranked, capped at 5. Losses/give-ups are not recorded.

## Configuration

No YAML fields of its own. `.hang` is always enabled; it implements `registry.Configurable`
only so its `Configure` step runs after config load, to pick the word bank matching
`Language`.

See also the shared [`games`](../../support/games/README.md) support layer.
