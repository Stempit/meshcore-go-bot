# words — anagram (Слова из слова)

Package `words` registers the `.words` command: a timed word-building game playable in
**DMs and channels**. The bot picks one long base word; players race to build shorter
words from its letters before a countdown runs out. Scoring is super-linear
(`score = len²`), so the game rewards digging for long, hard words. The dictionary
follows the bot's global `Language` setting: Russian nouns for `ru`, English words
otherwise — see [Dictionary](#dictionary-embedded) below.

> Example (`Language: ru`): base word `кальвадос` → `.words лава` → `✅ +16 очков` (4 letters, `4² = 16`).

`.words` is the first game with a **self-firing timer**: when the round expires the bot
*pushes* results to the channel/DM with no triggering message, via the injected
`games.Announcer` (wired in `cmd/bot/bot.go`).

## Command surface

| Input | Action |
|-------|--------|
| `.words` / `.words start` | Pick a base word and open a round (resets timer) |
| `.words <слово>` | Submit a word built from the base word's letters |
| `.words status` | Show base word, time left, and your score |
| `.words end` | Close the round early and show results now |
| `.words top` | Top winners in this scope over the last 30 days |

Every `.words <…>` command (start, submission, even `status`) **resets the countdown**
to its full length. Only `end` and natural timeout close the round. Scope keying is the
shared `games.Scope` (channel name, or `"dm:"+senderKey`); channels are
collaborative-competitive (shared base word, per-player scores), DMs are solo.

### Submission validation (in order, cheapest first)

1. Normalize (lowercase; `ru` folds `ё→е` and keeps only `а–я`, `en` keeps only `a–z`); empty → `❌ only Russian letters` / `❌ letters only`.
2. Length ≥ `min_sub_len` → else `❌ too short`.
3. Not the base word itself → `❌ that's the base word`.
4. Not already found this round → `❌ already found`.
5. **Multiset-buildable** from the base letters (one base letter backs one submission letter) → else `❌ word not found`.
6. In the dictionary → else `❌ word not found`.

Scoring uses **rune length**, not byte length. State is in-memory (lost on restart).

## Dictionary (embedded)

Two word lists are compiled into the binary via `go:embed`, and `Configure` picks one
at startup based on `config.Get().Language`:

- `words_anagram_ru.txt` (~67k ё-folded lowercase Russian nouns) for `Language: ru`.
- `words_en.txt` (~370k lowercase English words, from
  [dwyl/english-words](https://github.com/dwyl/english-words)) otherwise.

The chosen list is parsed once into two indexes: a validation set, and a base-word pool
of entries `base_min_len..base_max_len` runes long (default 8–10). The parser is
defensive (re-normalizes every line, drops anything under 3 runes or outside the
language's letter set). The bot only ever loads one language's dictionary per run —
switching `Language` requires a restart, same as the rest of the bot's i18n.

## Leaderboard (`.words top`)

Winners are recorded through the shared `games` support layer under
`log/games/words/channels/<scope>.txt`; `.words top` shows wins per player over the
last 30 days, ranked, capped at 5. Rounds with no finds are not recorded.

## Configuration

YAML key `words`. **Opt-in** — stays off until `enabled: true`. Zero-value ints are
defaulted at their use sites.

```yaml
words:
  enabled: false            # opt-in
  round_sec: 300            # countdown length (default 300 = 5 min)
  min_sub_len: 3            # shortest valid submission
  base_min_len: 8           # shortest base word
  base_max_len: 10          # longest base word
```

The end-of-round results line is built to a byte budget. If you want overlong lines to
wrap across messages instead of truncate, add `shrink.overrides.words: split` — that is
a config choice, not code here.

See also the shared [`games`](../../support/games/README.md) support layer.
