# shrink — centralized message fitting

MeshCore enforces a hard **140-byte** limit per message; replies over it are silently
dropped on the wire. Package `shrink` is the single place that knows the byte limit and
the strategies for staying under it. Its entire public surface is **one function**:

```go
// Fit turns a handler's logical messages into mesh-ready wire messages, fitting each
// via the variant configured for `name`, flattening, and prepending `prefix` to the
// first wire message. Nothing returned exceeds max_bytes. Never returns an error.
func Fit(name, prefix string, msgs []string) []string
```

`Fit` is called once per outgoing path (DM, channel, Telegram) in `cmd/bot`. Handlers
return **raw logical messages** and do not import `shrink`.

## Variants

| Variant | Behaviour |
|---------|-----------|
| `truncate` | hard cut to fit, rune-safe, with ellipsis |
| `split` | split into `[i/N]` parts (≤9 per message) |
| `llm` | LLM compress with retries; falls back to `split` on any failure |
| `skip` | drop a message that won't fit |

`truncate`/`split` share one rune- and space-aware boundary walker (cuts never land
mid-rune). `llm` retries against the exact budget using a dedicated shrink model, and
because it falls back to `split` (which always succeeds) there is no give-up message.

## What Fit does, in order

1. **Resolve the variant** for `name`: `overrides[name]` → `default` → hardcoded
   `truncate`.
2. **Fit each element independently**, then flatten. Slice length is the only "single vs
   list" signal — a one-element answer may split into `[i/N]`; an N-element list
   (`.news`, `.bbs`, picoclaw parts) fits each item on its own.
3. **Account for the prefix** (`@[sender] `, `[TG:sender] `) — only the first wire
   message carries it, fitted against `max_bytes − len(prefix)`.
4. **Guarantee the limit** — every returned message is `≤ max_bytes` as a backstop.

## Lossless pre-transform: Cyrillic → Latin

Before any variant runs, `cyr_to_lat` can substitute Cyrillic letters that are
pixel-identical to an ASCII twin (`а→a`, `е→e`, `о→o`, `с→c`, …), saving 1 byte each with
no visual change. If the squeezed text then fits, `Fit` returns it directly (for the
`llm` variant this means the LLM is never called). The set is deliberately conservative;
the LLM itself always receives the original Cyrillic, with the squeeze applied to its
output.

## Configuration

YAML key `Shrink` (plus the shared `LLM` provider config).

```yaml
Shrink:
  default: llm          # global default variant
  max_bytes: 140        # hard mesh limit
  ellipsis: "…"
  cyr_to_lat: true      # lossless Cyrillic→Latin squeeze
  overrides:            # per-name variant override (name = command or source key)
    claw: split
    news: skip
    bbs:  split
    telegram: split
  llm:                  # the "llm" variant's provider
    type: ollama        # optional; inherits LLM.type when empty
    model: <light-model> # optional; inherits LLM.model when empty
    prompt: "..."
    temperature: 0.3
    max_retries: 3
```

**Provider vs operation:** `LLM.*` describes *is there an LLM and how do I reach it*;
`Shrink.llm.*` describes *how to use it to shrink* — so a big model can answer while a
light model shrinks. `LLM.enabled` is the master switch; when false the `llm` variant
falls back to `split`.

## Layout

| File | Role |
|------|------|
| `fit.go` | `Fit`, variant resolution, config validation, final cap |
| `split.go` | rune/space-aware splitter (cap 9), marker helpers |
| `truncate.go` | rune-safe truncate with ellipsis |
| `llm.go` | LLM variant (wraps `internal/bridge/llm`), split fallback |
| `cyrlat.go` | lossless Cyrillic→Latin pre-transform |
| `shrink_test.go` | the repo's only unit tests |

`shrink` imports only `internal/config` and `internal/bridge/llm`.
