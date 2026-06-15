# Centralized message shrinking

MeshCore enforces a hard **140-byte** limit per message; replies over it are
silently dropped on the wire. Historically each command (and each non-command
send path) solved this on its own, producing three different splitters, two
LLM-shrink call sites, and a handful of bespoke config flags. This document
describes the centralized replacement: the `internal/shrink` package, whose
entire public surface is **one function**.

## Goals

- One place that knows the byte limit and the strategies for staying under it.
- A global **default** strategy with **per-command overrides** from config.
- A single mandatory call on every outgoing path, so nothing over 140 bytes can
  reach the radio.
- Correct accounting for the reply **prefix** (`@[sender] `, `[TG:sender] `, RSS
  feed prefix) that eats into the first message's budget.

## What it replaces

Three independent splitters (two not rune-safe) and two LLM-shrink sites, across
commands *and* the two non-command send paths:

| Old site | Old strategy | Rune-safe |
|---|---|---|
| `commands/llm.go` `ShrinkText` | LLM compress + retries | n/a |
| `commands/claw.go` `splitForMesh` | split into `[i/N]` | yes |
| `commands/bbs.go` `bbsSplitPost` | split, no markers | **no** |
| `commands/news.go` | `shrink` / `skip` / passthrough | n/a |
| `commands/weather.go` | LLM shrink | n/a |
| `cmd/bot/bot.go` `splitMeshMessage` | prefix-aware split | **no** |
| `cmd/bot/bot.go` Telegram bridge | shrink-or-split, `[TG:…]` prefix | — |

(`internal/rss/poller.go` has its own `prepareTitle` shrink logic but is dead
code — nothing imports it; the live RSS path is `cmd/news`, which only writes
titles to files, and `.news` shrinks them when serving. Left untouched.)

Centralizing collapses all three splitters into one rune-safe splitter, both LLM
sites into one, and fixes the not-rune-safe cuts for free. The bespoke selectors
`Claw.Split` (bool), `RSS.LongTitle` (string), and the per-source Telegram
`Shrink` (bool) all become variant choices.

**Out of scope: domain formatting.** `.path` abbreviating node names, and `.bbs`
truncating a thread title to 20–25 chars or a name to 8, are decisions about how
to *render a field*, not how to fit the mesh limit. They stay in their handlers
and run before the message reaches `shrink`.

## Variants

```go
type Variant string

const (
    Truncate Variant = "truncate" // hard cut to fit, rune-safe, ellipsis
    Split    Variant = "split"    // split into "[i/N]" parts (≤9, see cap)
    LLM      Variant = "llm"      // LLM compress w/ retries; falls back to Split
    Skip     Variant = "skip"     // drop a message that won't fit
)
```

- **`Truncate`** and **`Split`** share one rune- and space-aware boundary walker.
  Cuts never land mid-rune; a space in the back half of a chunk is preferred.
- **`LLM`** retries `Shrink.llm.max_retries` times against the exact budget,
  using a dedicated shrink model (see config). It falls back to **`Split`**
  whenever it can't deliver a short-enough result — LLM disabled, call error, or
  retries exhausted. `Split` always succeeds, so there is no give-up message.
- **`Skip`** drops any message that won't fit (the old `.news` skip behavior).

### Split cap: at most 9 parts

`Split` produces **no more than 9 messages**. Because `N ≤ 9`, the marker is
single-digit on both sides (`" [9/9]"` = 6 bytes), so the per-chunk reserve is 6.
If the text can't fit in 9 parts, the 9th is truncated (with the ellipsis) so the
series never exceeds 9. The cap is **per fitted message**, never across a reply
set — a 20-item news list is legitimately 20+ wire messages.

## Lossless pre-transforms

Before any variant runs, `Fit` can apply **lossless** byte reductions — squeezes
that change bytes but not the rendered text. They run *in front of* the variant
pipeline, so a message often drops under the limit without any cutting.

### Cyrillic → Latin homoglyphs (`cyr_to_lat`)

Many Cyrillic letters are pixel-identical to a Latin (ASCII) letter. Each
Cyrillic rune is 2 bytes in UTF-8; its ASCII twin is 1 byte, so every
substitution saves exactly one byte with no visual change. The set is
deliberately **conservative** — only glyphs identical in virtually every font:

```
lower: а→a  е→e  ё→e  о→o  с→c  р→p  у→y  х→x
UPPER: А→A  В→B  Е→E  Ё→E  К→K  М→M  Н→H  О→O  Р→P  С→C  Т→T  У→Y  Х→X
```

Lowercase `к м т н` (and similar near-misses) are **excluded**: they only
resemble their Latin counterparts.

Placement matters because of the **LLM** variant:

- For `truncate` / `split` / `skip` and the initial "does it already fit?" check,
  the transform is applied to the **input** before the budget is computed. If the
  squeezed text fits, `Fit` returns it directly — for the `llm` variant this means
  the LLM is never called.
- For the `llm` variant the LLM receives the **original** Cyrillic (clean input →
  good compression); the transform is then applied to each candidate **response**
  before the budget check, and to the text handed to the `split` fallback. Feeding
  the model pre-substituted mixed-script text would corrupt its input, so the
  squeeze stays on the output side.

The reply **prefix** (`@[sender] `, `[TG:sender] `) is squeezed too, and its
post-transform length is what the first message's budget accounts for.

Controlled by a single global `Shrink.cyr_to_lat` bool (no per-name override).

## The one function

```go
// Fit turns a handler's logical messages into mesh-ready wire messages:
// fits each element via the variant configured for `name`, flattens the result,
// and prepends `prefix` to the first wire message. Nothing returned exceeds
// max_bytes. prefix is "" when there is none (DMs, bot-originated posts).
func Fit(name, prefix string, msgs []string) []string
```

That is the whole public API. Everything else is internal.

What `Fit` does, in order:

1. **Resolve the variant** for `name` (override → default → `Truncate`).
2. **Fit each element of `msgs` independently** with that variant, then flatten.
   - One long answer arrives as a 1-element slice → may split into `[i/N]`.
   - A list (`.news` items, `.bbs` posts, picoclaw parts) arrives as an
     N-element slice → each item is fitted on its own; markers and the 9-part cap
     stay scoped to that item. **The slice length is the only "single vs list"
     signal needed** — no separate API.
3. **Account for the prefix.** Only the very first wire message carries the
   prefix, and `Fit` knows which one that is (it owns ordering). It fits the first
   message against `max_bytes − len(prefix)` so that after prepending the prefix
   the result lands at exactly `≤ max_bytes`. Every other message uses the full
   budget. This is why there is no separate guard step, no `headReserve`
   threading, and no post-hoc marker-preserving truncation: the first chunk is
   given the right budget *before* it is split.
4. **Guarantee the limit.** As a final backstop every returned message is
   `≤ max_bytes` (e.g. an absurdly long prefix that eats the whole budget is
   hard-cut here).

`Fit` never returns an error: every failure mode of the LLM variant falls back to
`Split`, which always succeeds.

## Configuration

```yaml
LLM:                    # provider: "is there an LLM, how do I reach it"
  enabled: true
  type: openrouter      # provider for full answers
  model: <big-model>
  system_prompt: ...
  system_temperature: 0.7

Shrink:
  default: llm          # global default variant
  max_bytes: 140        # hard mesh limit
  ellipsis: "…"
  cyr_to_lat: true      # lossless Cyrillic→Latin homoglyph squeeze (1 byte/char)
  overrides:            # per-name override of `default`
    claw: split
    news: skip
    bbs:  split
    telegram: split
    rss:  skip
  llm:                  # the "llm" variant: "how do I use it to shrink"
    type: ollama        # optional; inherits LLM.type when empty
    model: <light-model>  # optional; inherits LLM.model when empty
    prompt: ...
    temperature: 0.3
    max_retries: 3
```

**Provider vs operation.** `LLM.type` / `LLM.model` / `LLM.enabled` describe the
provider — *is there an LLM and how do I reach it.* `Shrink.llm.*` describes the
shrink operation — *how do I use it to shrink.* The split lets a big model answer
while a light model shrinks: `Shrink.llm.type` / `Shrink.llm.model` override the
provider for shrinking only and inherit from `LLM` when empty. `LLM.enabled`
stays the master switch — when false the `llm` variant falls back to `Split`.

There is no `target_bytes`: `Fit` hands each message its exact budget
(`max_bytes`, minus the prefix on the first), and the LLM retries against that.
There is no `fail_message`: `Split` is the fallback and always succeeds.

## Override resolution

`Fit` resolves the variant in this order:

1. `Shrink.overrides[name]` — the per-name override.
2. `Shrink.default` — the global default.
3. hardcoded `Truncate` — safe last resort.

`name` is any context key: a command name (`weather`, `news`, …) or a
non-command source (`telegram`, `rss`). To force a command's variant the user
sets it in `overrides` — e.g. `bbs: split` keeps BBS user content from being
LLM-reworded. At config load, `default` and every `overrides` value are validated
against the known variants; an unknown variant is logged and ignored (falls
through to `default`).

## Send sites

`Fit` is called once per outgoing path. Handlers return **raw logical messages**
and no longer import `shrink`; `GenerateReplies` returns the matched command name
so `cmd/bot/bot.go` can pass it through:

```go
replies, name, err := commands.GenerateReplies(...)
// DM command reply:
out := shrink.Fit(name, "", replies)
// channel command reply:
out := shrink.Fit(name, "@["+senderName+"] ", replies)
```

The Telegram bridge (a non-command path) passes its own name and prefix:

```go
// Telegram bridge (cmd/bot/bot.go):
out := shrink.Fit("telegram", "[TG:"+senderName+"] ", []string{text})
```

The prefix lands on the first wire message only — including Telegram split parts
(standardized from the old per-part repetition). Variant selection that used to
be a bool/string flag (`tgbridge` `Shrink`, `RSS.LongTitle`) is now the
`overrides` entry for `telegram` / `news`. Short fixed replies (advert/busy
acknowledgements) are sent directly without `Fit`.

## Package layout & dependencies

```
internal/shrink/
  fit.go       – Fit, variant resolution, config validation, final cap
  split.go     – rune/space-aware splitter (cap 9), marker helpers
  truncate.go  – rune-safe truncate with ellipsis
  llm.go       – LLM variant (wraps internal/llm), Split fallback
  cyrlat.go    – lossless Cyrillic→Latin homoglyph pre-transform
```

`shrink` imports only `internal/config` and `internal/llm`. It is imported by
`cmd/bot` — **not by `commands`**, since handlers return raw text and all
shrinking happens at the send sites. No import cycle: `llm` does not import
`config`.

## Migration

- Create `internal/shrink` with `Fit` and the four variants.
- Add the `Shrink` config struct (`internal/config/model.go`). Move the LLM
  shrink params into `Shrink.llm`. Remove `Claw.Split`, `RSS.LongTitle`, the
  per-source Telegram `Shrink` bool, `LLM.shrink_prompt` /
  `LLM.shrink_temperature` / `LLM.shrink_max_retries`, and the old fail message.
- `GenerateReplies` returns the matched command name (`([]string, name, error)`).
- Delete `ShrinkText`, `splitForMesh`, `bbsSplitPost`, `splitMeshMessage`,
  `prepareTitle`, and the RSS `shrinkFunc` callback. Handlers stop importing
  `shrink` and return raw logical `[]string`. Leave `.path` / `.bbs` domain
  formatting in place.
- Call `shrink.Fit` at the send sites in `cmd/bot/bot.go` (DM, channel,
  Telegram). `internal/rss` is dead code and is left untouched.

## Notes

- All length math is in **bytes** (`len`), because the 140 limit is bytes;
  Cyrillic is 2 B/char. Cuts always respect **rune** boundaries.
- `Fit` runs on every outgoing message and is LLM-free unless the resolved
  variant is `llm`.
