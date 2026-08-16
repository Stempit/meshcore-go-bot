# hang — hangman (Виселица)

Package `hang` registers the `.hang` command: a letter-guessing word game playable in
**DMs and channels**. The bot picks a hidden Russian word from an embedded word bank
(`words_ru.txt`); players reveal it one letter at a time before the 6 lives run out.

The masked word, used letters, and lives must all fit in one 140-byte mesh message,
which is why the bank is kept to short words.

## Scope: DM and channel share one mechanic

Same shared scope keying as [`.num`](../num/README.md): channel name, or
`"dm:" + senderKey` for DMs.

- In a **channel** the game is **collaborative**: shared word, shared lives; the player who reveals the last letter is credited by the send layer's `@[Sender]` prefix.
- In a **DM** it is solo.

## Command surface

| Input | Action |
|-------|--------|
| `.hang` / `.hang start` | Pick a random word and open a game |
| `.hang <буква>` | Guess one Cyrillic letter |
| `.hang <слово>` | Guess the whole word |
| `.hang status` | Show current mask, used letters, lives |
| `.hang end` | Give up and reveal the word |
| `.hang top` | Top winners in this scope over the last 30 days |

## Russian-specific rules

- **`ё` → `е`** normalization on both word and guess, so `ё` never needs typing.
- Lowercase, case-insensitive.
- Non-Cyrillic input → `Только русские буквы`.
- A duplicate letter does not cost a life.
- Mask rendered with no spaces (`к__к_`) to save bytes.

State is in-memory (lost on restart). The word bank is embedded with `go:embed`
(nouns, ё-stripped) so it is always present and needs no deploy step.

## Leaderboard (`.hang top`)

Wins are recorded through the shared `games` support layer under
`log/games/hang/channels/<scope>.txt`; `.hang top` shows wins per player over the last
30 days, ranked, capped at 5. Losses/give-ups are not recorded.

## Configuration

None. `.hang` is always enabled.

See also the shared [`games`](../../support/games/README.md) support layer.
