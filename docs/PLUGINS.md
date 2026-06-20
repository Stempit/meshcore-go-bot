# Plugin-style commands — implementation plan

Status: **implemented**. The flat `internal/commands` package has been converted
into self-registering, self-contained command modules ("plugins") under
`internal/commands/<name>/`, driven by the `internal/support/registry`
framework. This document remains the design reference; see
[NEW_COMMAND.md](NEW_COMMAND.md) for the day-to-day "add a command" workflow.

> Note: the `.help` index lists commands in registration order, which the Go
> runtime fixes as lexicographic import-path order, so the generated index is
> alphabetical by trigger rather than the original hand-curated order.

## Goal

Today every command lives as a loose `.go` file in `internal/commands`, and adding
one means editing three places:

- the central `switch` in [`internal/commands/commands_server.go`](../internal/commands/commands_server.go)
- the parallel `switch` in [`internal/commands/help.go`](../internal/commands/help.go)
- the monolithic `Config` struct in [`internal/config/model.go`](../internal/config/model.go)

We want each command to be **one folder** that owns its code, its config struct,
its help text, and its assets — and to register itself, so adding a command is
"create a folder + add one import line."

## Non-goal: Go runtime plugins

We are **not** using Go's `plugin` package (`.so` loading). It is effectively
Linux-only, brittle across compiler/dependency versions, and would cost us
`go:embed`, static binaries, and cross-compilation. "Plugin" here means a
**compile-time self-registering module**. No behaviour is loaded at runtime.

---

## Target layout

```
internal/commands/
  registry/            – the framework: Ctx, Meta, Command, Register, Dispatch, Help
  allcommands/         – aggregator: blank imports of every command package
  ping/      ping.go
  trace/     trace.go
  weather/   weather.go + config.go
  words/     words.go + config.go + words_anagram_ru.txt (go:embed)
  bbs/       bbs.go            (stateful interceptor, Priority > 0)
  claw/      claw.go           (custom matcher for @[BotName] mention)
  ...
```

Games (`internal/games`) become plugins under the same registry. The shared game
helpers in [`internal/support/games/game.go`](../internal/support/games/game.go) (`Announcer`,
`gameScope`, `gameTop`, `logWin`) stay as a support package that game-plugins
import; only the command-routing layer moves.

**Caveat — those helpers are unexported today, so this step is not free.** Moving a
game handler (e.g. `ProcessWordsCommand`) into its own package means it can no
longer see `games.gameScope`/`logWin`/`wordsLog`/`gameTop`, and a nested
`internal/games/<game>` subpackage can't reach them either (Go subpackages can't
see a parent's unexported names). So you must either **export** the helper surface
(`Scope`, `LogWin`, `Top`, the per-game log handles) or keep the handler in
`package games` behind a thin plugin shim. The inline config reads inside the
handler move with it too — [`words.go`](../internal/games/words.go) reads
`RoundSec`/`MinSubLen`/`BaseMinLen`/`BaseMaxLen` at use sites, and those become the
words plugin's `Configure`. This is the highest-effort migration step.

---

## The registry package

A new package `internal/registry` with no dependency on any individual
command (commands depend on it, never the reverse — this breaks the import cycle).

### Ctx — the unified handler input

Replaces the divergent signatures today (`ProcessPingMessage()`,
`ProcessLastCommand(text, channelName)`,
`ProcessNumCommand(text, senderKey, senderName, channelName)`, etc.).

```go
type Ctx struct {
    Text       string          // raw incoming text
    Trimmed    string          // strings.TrimSpace(Text)
    Lower      string          // strings.ToLower(Trimmed)
    Channel    string          // channel name; "" for DMs
    SenderKey  string          // stable per-sender id (see encoding note)
    SenderName string          // human-readable author; may be ""
    PingMeta   *PingMeta       // moved here from package commands
    Cfg        *config.Config  // shared/infra config (read-only)
}
```

**`SenderKey` is not a uniform encoding today — the `Ctx` constructor must
reproduce the current values verbatim.** It is a map/scope key, so changing it
silently re-keys persistent state:

- DM: `string(contact.PublicKey)` — raw pubkey bytes as a string (NOT hex), see the
  DM path in [`process.go`](../internal/dispatch/process.go).
- Channel: `fmt.Sprintf("%x", c.PublicKey)` — hex of the pubkey, or `""` when the
  sender's contact is unknown.

It feeds BBS sessions, tickets, and — critically — the persisted DM leaderboard
path via `gameScope` → `"dm:"+SenderKey` → `log/games/<game>/`. "Cleaning it up" to
one encoding would fork every existing DM leaderboard file, so the migration keeps
the per-path encoding as-is.

Helper methods keep handlers terse:

```go
func (c Ctx) Arg(prefix string) string  // TrimSpace(TrimPrefix(Trimmed, prefix))
func (c Ctx) IsDM() bool                 // Channel == ""
```

`PingMeta` (and `buildPingMeta`/`setSelfCoords` consumers in
[`internal/dispatch/process.go`](../internal/dispatch/process.go)) move into the
registry package (or a small shared `commands/types` package) so both dispatch and
plugins reference one definition.

### Meta — declarative description

```go
type MatchMode int
const ( Exact MatchMode = iota; Prefix )

// Prefix semantics are "exact trigger OR trigger followed by a space":
//   ctx.Lower == t || strings.HasPrefix(ctx.Lower, t+" ")
// The trailing space is load-bearing. A naive strings.HasPrefix(ctx.Lower, t)
// would let ".w" (weather) swallow ".words" — HasPrefix(".words", ".w") is true.
// Today's switch already encodes this exact-or-space form for the collision-prone
// commands; the few that use a bare HasPrefix (.news/.last/.llm/.claw/.8ball)
// collapse onto the same stricter rule (only pathological glued args like
// ".8ballhi" stop matching, which is acceptable).
```

```go
type Meta struct {
    Name      string         // "ping" — also the shrink.Fit variant key
    Triggers  []string       // ".ping", "ping", "пинг"
    Match     MatchMode      // Exact | Prefix (applies to all Triggers)
    HelpShort string         // listed in .help index (needs Group too)
    HelpLong  string         // shown by ".<cmd> help"
    Group     string         // .help index category (e.g. "Games"); empty → not listed
    Priority  int            // higher runs first; interceptors (BBS) use this
}
```

### Command — the interface every plugin implements

```go
type Command interface {
    Meta() Meta
    Handle(Ctx) ([]string, error)
}
```

Two **optional** interfaces, checked via type assertion at registration:

```go
// Custom matching beyond Triggers/Match (claw mention, BBS-mode interception).
// A Matcher must reproduce ALL of the command's current entry points: the claw
// plugin matches both the "@[BotName]" mention AND the ".claw" trigger; the bbs
// plugin matches ".2ch" (enter) AND any input while IsBBSMode(SenderKey) is true.
type Matcher interface {
    Matches(Ctx) bool   // if implemented, used INSTEAD of Triggers
}

// Per-plugin config + enable gate (see config section below).
type Configurable interface {
    ConfigKey() string                 // "words"
    Configure(config.Sectioner) error  // decode + defaults + validate
    Enabled() bool                     // false hides from routing AND help
}
```

### Registration + dispatch

```go
func Register(c Command)               // called from each plugin's init()
func ConfigureAll(s config.Sectioner)  // main calls after config.Load
func Dispatch(ctx Ctx) (replies []string, name string, err error)
func HelpIndex() string                // grouped index, generated from registered Meta
```

`Dispatch` logic (replaces the big switch):

1. Per-command help: if `ctx.Lower == "<trigger> help"` for an enabled command
   with a non-empty `HelpLong`, return that `HelpLong`. This runs first so it
   beats the claw/bbs `Matcher` interceptors.
2. Sort registered commands by `Priority` desc, then registration order.
3. For each command: skip if `Configurable` and `!Enabled()`.
4. Match: if it implements `Matcher`, call `Matches(ctx)`; else compare
   `ctx.Lower` against `Triggers` per `Match` mode (see Prefix semantics above).
5. First match wins → return `c.Handle(ctx)` plus `c.Meta().Name`.
5. No match → `nil, "", nil` (same contract as today).

**Interceptor precedence must be pinned by `Priority`,** because order is meaningful
today: claw-mention is checked *before* BBS, so a `@[Bot]` mention wins even while a
user is in BBS mode. Use e.g. `claw=20 > bbs=10 > normal=0` so the sort reproduces
the existing precedence.

Help is generated by iterating the registry, so `help.go`'s second switch and the
hand-maintained `helpList` constant both disappear.

---

## Config: how per-command config works

The user-facing `config.yaml` stays a **single file**. Only the Go ownership of each
section moves next to its command. Mechanism is **two-pass YAML decoding**.

### Pass 1 — central loader keeps raw sections

Extend [`internal/config/config.go`](../internal/config/config.go). Today it does
one `yaml.Unmarshal` into `Config`. Add a raw-node map and a `Sectioner`:

```go
var rawSections map[string]yaml.Node   // filled during Load

type Sectioner interface { Section(key string, target any) error }

func (s sectioner) Section(key string, target any) error {
    n, ok := rawSections[key]
    if !ok { return nil }       // absent section → caller's defaults stand
    return n.Decode(target)
}
```

`Load` unmarshals the document into both `Config` (for infra) **and**
`map[string]yaml.Node` (for plugin sections) in the same pass.

### Pass 2 — each plugin decodes its own section

The plugin owns its struct, defaults, and gate in its folder, e.g.
`internal/commands/words/config.go`:

```go
type cfg struct {
    Enabled  bool   `yaml:"enabled"`
    DictPath string `yaml:"dict_path"`
    RoundSec int    `yaml:"round_sec"`
    // ...
}
var conf cfg

func (p *plugin) ConfigKey() string { return "words" }
func (p *plugin) Configure(s config.Sectioner) error {
    if err := s.Section("words", &conf); err != nil { return err }
    if conf.DictPath == "" { conf.DictPath = "words_anagram_ru.txt" }
    if conf.RoundSec == 0  { conf.RoundSec = 300 }
    return nil
}
func (p *plugin) Enabled() bool { return conf.Enabled }
```

The package-level `var conf cfg` is acceptable: `ConfigureAll` runs once at startup
before the dispatch loop, so it is configured-once then read-only (same discipline
as the `config.Get()` singleton).

This also pulls today's scattered "default at use-site when zero" logic (noted for
`WordsConfig`) into one place per command.

### Tiering — what stays central vs moves

- **Stays in `config.Config`** (infra, not commands; read via `Ctx.Cfg`):
  `Connection`, `Proxy`, `MQTT`, `Telegram`, `Security`, `Shrink`, `LLM`, **`Trace`**.
  `Trace` stays central even though `.trace` is a command: `setSelfCoords` in
  [`dispatch/process.go`](../internal/dispatch/process.go) reads it while building
  `PingMeta`, *before* dispatch knows which command will run. Moving it into the
  `trace` plugin would force `dispatch` to import an individual command package —
  exactly the coupling this design forbids.
- **Moves to plugin folders** (per-command sections): `Weather`, `Top`, `Words`,
  `Claw`, `Stats`. Each becomes a struct beside its handler. Note `Stats` (the
  stats.json data source) is owned by the **`path`** plugin that reads it
  ([`path.go`](../internal/commands/path.go)), *not* the unrelated `.stat`
  channel-traffic command — the name clash is a trap.

### Startup order (main)

1. `init()` of each plugin → `registry.Register(...)` (before `main`).
2. `config.Load("config.yaml")` → pass 1 (infra struct + raw nodes).
3. `registry.ConfigureAll(config.Sectioner())` → pass 2 per plugin; **fail loud**
   on malformed/invalid section *values*.
4. Dispatch loop runs; disabled plugins are invisible to routing and help.

Sharp edge: "fail loud" only catches a malformed **value** inside a correctly-named
section (its `Decode` errors). A misspelled section **key** (`weathr:` for
`weather:`) fails *silently* — `Section("weather")` finds nothing, returns nil, and
the plugin runs on defaults — exactly as the monolithic unmarshal does today
(yaml.v3 ignores unknown keys). To genuinely catch typo'd keys, track which
`rawSections` were consumed by `ConfigureAll` and error on any leftover top-level
section (or decode infra with `KnownFields(true)`). Dumping registered
`ConfigKey()`s at boot helps diagnose. The `sync.Once` singleton is unaffected;
hot-reload (if ever wanted) is just re-running pass 1 + `ConfigureAll`.

---

## The awkward cases (the real test of the design)

| Case | Today | In the design |
|------|-------|---------------|
| BBS stateful interception (`IsBBSMode` claims all input) | pre-switch block in `commands_server.go` | `bbs` plugin, `Priority` above all normal commands; `Matcher.Matches` returns true for `.2ch` or while the sender is in BBS mode |
| Claw `@[BotName]` mention | pre-switch block | `claw` plugin `Matcher` matches the mention prefix *and* `.claw`; `Priority` above `bbs` to keep mention-beats-BBS |
| `words.enabled` gate | `if !config.Get().Words.Enabled` in switch | `Configurable.Enabled()` |
| Command name for `shrink.Fit` variant | second return value of `GenerateReplies` | `Meta().Name`, returned by `Dispatch` |
| Game timer announcements | `games.SetAnnouncer(d.AnnounceToScope)` | unchanged; still injected at startup into the games support package |

The advert command (`commands.ParseAdvertCommand`) is handled in `process.go`
**before** `GenerateReplies` and is not a text command — leave it where it is, or
later model it as a `Priority`-high `Matcher`. Out of scope for the first pass.

---

## Dispatch integration

[`internal/dispatch/process.go`](../internal/dispatch/process.go) calls
`commands.GenerateReplies(...)` in two places (DM and channel). They become:

```go
replies, name, err := registry.Dispatch(registry.Ctx{
    Text: m.Text, Channel: channel.Name, PingMeta: pingMeta,
    SenderKey: senderKeyHexCh, SenderName: m.SenderName, Cfg: config.Get(),
    // Trimmed/Lower filled by a Ctx constructor
})
```

The `name` (variant) and `replies` feed `shrink.Fit(name, prefix, replies)`
exactly as today — no change downstream.

---

## Migration: incremental, always buildable

Big-bang is risky (essentially no automated tests — only
[`internal/shrink`](../internal/shrink) has one). Run **both** dispatchers during
migration:

**Phase 0 — scaffold.** Add `registry` package + `config.Sectioner`/raw-node
support. No behaviour change. `go build ./... && go vet ./...`.

**Phase 1 — bridge.** Inside `commands.GenerateReplies`, slot `registry.Dispatch`
**in place of the switch only** — *after* the existing claw-mention and BBS
pre-switch blocks — and on no-match (`name == ""`) fall through to the legacy
switch. Do **not** put `Dispatch` at the very top: until `bbs`/`claw` are migrated
the registry knows nothing about BBS/mention state, so an already-migrated command
(say `ping`) would otherwise answer a user who is mid-BBS-session and bypass the
interceptor. Wire `registry.ConfigureAll` into `main` after `config.Load`. Now both
paths coexist. (Alternative: migrate `bbs`/`claw` *first* with their `Priority`, so
the registry itself intercepts before any migrated command — but keeping the legacy
pre-blocks during the bridge is simpler and equivalent.)

**Phase 2 — migrate one at a time.** Per command: create folder, move handler,
implement `Command` (+ `Configurable` if it has config), add blank import to
`allcommands`, delete its `case` from the switch and its entry from `help.go`.
Build + manually smoke-test via `cmd/example` / live. Suggested order:

1. `8ball` (trivial, no config) — proves the framework end to end.
2. `ping`, `trace`, `last`, `top`, `stat` (simple).
3. `weather`, `news`, `path`, `llm`, `ticket` (config / state).
4. games: `dice`, `num`, `hang`, `words` (Announcer + leaderboards + embed).
5. interceptors: `claw` (Matcher), `bbs` (Priority + Matcher).

Fidelity check per command: `Meta().Name` must equal the current `shrink.Fit`
variant key, which is decoupled from the trigger — `"weather"` (not `"w"`),
`"8ball"`, `"claw"`, `"bbs"`. Getting it wrong silently changes the shrink variant.

`greet` is **not** in this list: it has no text trigger and never flows through
`GenerateReplies` — it is event-driven in [`internal/greeter`](../internal/greeter)
(`onAdvert`/`onContact`). Like `ParseAdvertCommand`, it stays put.

**Phase 3 — remove the bridge.** When the switch is empty, delete
`commands_server.go`'s switch and the old `help.go`; `Dispatch` becomes the only
path. Update [`docs/NEW_COMMAND.md`](NEW_COMMAND.md) to the new "make a folder"
workflow.

After every phase: `go build ./... && go vet ./...` must pass.

---

## Open questions to confirm before coding

1. **Package home for `Ctx`/`PingMeta`/`Meta`** — put them in
   `internal/registry`, or a tiny `internal/commands/types` to avoid
   `dispatch` importing the registry's `Register`? (Leaning: registry; dispatch
   only needs `Ctx`, `Dispatch`, `PingMeta`.)
2. **Games support package** — keep `internal/games` as the shared helper home
   (Announcer, scope, leaderboard), with each game plugin under
   `internal/commands/<game>`? Either way the shared helpers must be **exported**
   (see the caveat under "Target layout") — a subpackage can't reach the parent's
   unexported names. Open part: export the helper surface vs. keep handlers in
   `package games` behind a thin shim.
3. **Config tiering — resolved.** `Trace` **stays central** (dispatch reads it via
   `setSelfCoords` before routing). `Stats` moves but is owned by the `path` plugin,
   not `.stat`. Remaining per-command sections: `Weather`, `Top`, `Words`, `Claw`,
   `Stats`.
