# registry — the command framework

Package `registry` is the self-registering command framework. Command plugins depend on
it; it never depends on them, which breaks the import cycle. Each plugin calls `Register`
from its `init()`; [dispatch](../../dispatch/README.md) and `main` only reference `Ctx`,
`Dispatch`, `ConfigureAll`, and the help helpers.

> "Plugin" here means a **compile-time self-registering module**, not Go's runtime
> `plugin` package. Nothing is loaded at runtime.

## Core types

**`Ctx`** — the unified handler input: `Text`, `Trimmed`, `Lower`, `Channel` (`""` for
DMs), `SenderKey`, `SenderName`, `PingMeta`, `Cfg`, plus helpers `Arg(prefix)` and
`IsDM()`.

> `SenderKey` is a map/scope key with a **non-uniform encoding** that must be preserved:
> DM = raw pubkey bytes as a string (not hex); channel = hex of the pubkey. It keys BBS
> sessions, tickets, and the persisted DM leaderboard paths — changing it silently
> re-keys state.

**`Meta`** — declarative description: `Name` (also the `shrink.Fit` variant key, decoupled
from the trigger), `Triggers`, `Match` (`Exact` | `Prefix`), `HelpShort`, `HelpLong`,
`Group` (empty → not listed in `.help`), `Priority`.

> `Prefix` means "exact trigger **or** trigger followed by a space" — the trailing space
> is load-bearing, so `.w` (weather) can't swallow `.words`.

**`Command`** — the interface every plugin implements:

```go
type Command interface {
    Meta() Meta
    Handle(Ctx) ([]string, error)
}
```

Two optional interfaces, checked by type assertion:

- **`Matcher`** — `Matches(Ctx) bool`, used *instead of* `Triggers` for custom matching
  (agent's `@[Bot]` mention, bbs-mode interception). Order interceptors with `Priority`
  (agent 20 > bbs 10 > normal 0).
- **`Configurable`** — `ConfigKey()`, `Configure(config.Sectioner)`, `Enabled()`; a
  false `Enabled()` hides the command from routing *and* help.

## Dispatch

```go
func Register(c Command)               // from each plugin's init()
func ConfigureAll(s config.Sectioner)  // main calls after config.Load
func Dispatch(ctx Ctx) (replies []string, name string, err error)
func HelpIndex() string                // grouped index, generated from Meta
```

`Dispatch`:

1. **Per-command help first** — `ctx.Lower == "<trigger> help"` returns `HelpLong`
   (before any matcher, so it beats agent/bbs interceptors).
2. Sort by `Priority` desc, then registration order.
3. Skip disabled `Configurable`s.
4. Match via `Matches` if implemented, else `Triggers`/`Match`.
5. First match wins → `Handle` + `Meta().Name`. No match → `nil, "", nil`.

`.help` and routing are both generated from the registered `Meta` — there is no central
switch or help list.

## See also

- Adding a command: [docs/NEW_COMMAND.md](../../../docs/NEW_COMMAND.md).
- Redirect gate (steering flood-prone commands off busy channels): `redirect.go`.
- Design background: this package replaced a flat `commands` package with a big switch.

## Files

| File | Role |
|------|------|
| `registry.go` | `Register`, `Dispatch`, `ConfigureAll`, match modes |
| `ctx.go` | `Ctx`, `PingMeta`, helpers |
| `help.go` | `HelpIndex` (grouped index from `Meta`) |
| `redirect.go` | channel-redirect gate (`Redirect` config) |
