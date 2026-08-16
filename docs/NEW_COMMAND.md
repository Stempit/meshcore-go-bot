# Adding a New Command

Commands are **self-registering plugins**: one folder that owns its handler,
its config, its help text, and its assets. Adding a command is "create a folder
+ add one import line." See [internal/support/registry/README.md](../internal/support/registry/README.md) for the framework design.

## How commands work

Incoming messages flow through `registry.Dispatch` in
[internal/dispatch/process.go](../internal/dispatch/process.go):

```go
replies, name, err := registry.Dispatch(registry.NewCtx(
    text, channelName, senderKey, senderName, pingMeta, config.Get()))
```

`Dispatch` sorts the registered commands by `Priority` (desc), then for each
enabled command compares `Ctx.Lower` against its `Triggers` (or calls its
`Matches` method). The first match wins; it returns the handler's replies and
`Meta().Name` (used by the send layer to pick a mesh-fit variant). No match →
`nil, "", nil`.

Every command implements [`registry.Command`](../internal/support/registry/registry.go):

```go
type Command interface {
    Meta() Meta
    Handle(Ctx) ([]string, error)
}
```

`Ctx` carries the normalized input (`Text`, `Trimmed`, `Lower`, `Channel`,
`SenderKey`, `SenderName`, `PingMeta`, `Cfg`) plus helpers `Arg(prefix)` and
`IsDM()`. `Channel` is empty for DMs.

---

## Step 1 — Create the plugin folder

Create `internal/commands/mycmd/mycmd.go`:

```go
// Package mycmd answers .mycmd <arg>.
package mycmd

import (
	"strings"

	"meshbot/internal/support/registry"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "mycmd",                       // shrink.Fit variant key
		Triggers:  []string{".mycmd"},            // Triggers[0] shows in .help
		Match:     registry.Prefix,               // Exact | Prefix
		HelpShort: "what it does",                // non-empty + Group → listed in .help
		Group:     "Service",                     // .help index category
		HelpLong:  ".mycmd <arg>\nwhat it does",  // shown by ".mycmd help"
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	rest := c.Arg(".mycmd")
	if rest == "" {
		return []string{"usage: .mycmd <arg>"}, nil
	}
	return []string{"result: " + rest}, nil
}
```

**Rules:**
- `Match: registry.Exact` matches only `ctx.Lower == trigger`. `registry.Prefix`
  matches `trigger` exactly OR `trigger` followed by a space (the trailing space
  is load-bearing, so `.w` can't swallow `.words`).
- Strip the prefix with `c.Arg(".mycmd")` (or `strings.TrimPrefix` +
  `strings.TrimSpace`), never `strings.ReplaceAll`.
- Return a usage string (not an error) for bad arguments.
- Log via `logging.For("commands")` or `slog` — structured key/values, English,
  no `log.Fatal`.

---

## Step 2 — Register it for compilation

Add one blank import to
[internal/commands/allcommands/allcommands.go](../internal/commands/allcommands/allcommands.go):

```go
_ "meshbot/internal/commands/mycmd"
```

That's the only edit outside your folder. The `init()` in your package calls
`registry.Register`, and `main` blank-imports `allcommands`.

---

## Step 3 — Per-command config (if needed)

Config stays a **single `config.yaml`**, but each plugin owns its section via
two-pass YAML. Add `internal/commands/mycmd/config.go`:

```go
package mycmd

import "meshbot/internal/config"

type cfg struct {
	Enabled    bool   `yaml:"enabled"`
	SomeOption string `yaml:"some_option"`
}

var conf cfg

func (plugin) ConfigKey() string { return "mycmd" }

func (plugin) Configure(s config.Sectioner) error {
	if err := s.Section("mycmd", &conf); err != nil {
		return err // fail loud on a malformed value
	}
	if conf.SomeOption == "" {
		conf.SomeOption = "default" // defaults live here, once
	}
	return nil
}

func (plugin) Enabled() bool { return true } // false hides from routing AND help
```

`Configure` runs once at startup (`registry.ConfigureAll` in `main`, after
`config.Load`), so `conf` is configured-once then read-only. Document new keys
in `config.example.yaml`.

**Infra config** (`Connection`, `Proxy`, `MQTT`, `Telegram`, `Security`,
`Shrink`, `LLM`, `Trace`) stays in `config.Config` and is read via `Ctx.Cfg`.

---

## Optional interfaces

- **`Matcher`** — custom matching beyond `Triggers` (e.g. an `@[Bot]` mention or
  stateful interception). When implemented it is used *instead* of `Triggers`.
  Use `Priority` to order interceptors (claw=20 > bbs=10 > normal=0). See
  [claw](../internal/commands/claw/claw.go) and [bbs](../internal/commands/bbs/bbs.go).
- **`Configurable`** — the config + enable gate from Step 3.

---

## Help

`.help` (grouped index) is generated from the registered `Meta` by the
[help](../internal/commands/help/help.go) plugin — no list to maintain. Set
`HelpShort` + `Group` (to appear in the index) and `HelpLong` (shown by
`.mycmd help`, intercepted centrally in `registry.Dispatch`).

---

## Using PingMeta / history

`c.PingMeta` carries path/timing/GPS (see
[registry/ctx.go](../internal/support/registry/ctx.go)). Read channel history
with `historylog.ReadLastChannel("log/history", c.Channel, n)`.

---

## Checklist

- [ ] `internal/commands/mycmd/mycmd.go` — `Meta` + `Handle` + `init` register
- [ ] `internal/commands/mycmd/config.go` — if config is needed
- [ ] `allcommands/allcommands.go` — one blank import line
- [ ] `config.example.yaml` — if config is needed
- [ ] Update the commands table in `CLAUDE.md`
- [ ] `go build ./... && go vet ./...` pass
