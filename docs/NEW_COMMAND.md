# Adding a New Command

## How commands work

All incoming messages flow through `GenerateReplies` in [internal/commands/commands_server.go](internal/commands/commands_server.go):

```go
func GenerateReplies(text, channelName string, pingMeta *PingMeta, senderKey, senderName string) ([]string, string, error)
```

The function normalizes text to lowercase, then routes to handler functions via a `switch` statement using `strings.HasPrefix`. It returns the reply messages, the **command name** (used by the send layer to pick a mesh-fit variant), and an error. Returning `nil, "", nil` means "no match, send nothing."

Handler functions themselves return `([]string, error)`; the dispatcher adds the command-name string when it returns.

Both DM and channel messages go through this same function. `channelName` is empty for DMs.

---

## Step 1 — Create the handler file

Create `internal/commands/mycmd.go`:

```go
package commands

import "strings"

func ProcessMyCommand(text string) ([]string, error) {
    rest := strings.TrimSpace(strings.TrimPrefix(text, ".mycmd"))
    if rest == "" {
        return []string{"usage: .mycmd <arg>"}, nil
    }
    return []string{"result: " + rest}, nil
}
```

**Rules:**
- Strip the command prefix with `strings.TrimPrefix` + `strings.TrimSpace` (never `strings.ReplaceAll`).
- Parse arguments with `strings.Fields(rest)`.
- Return a usage string (not an error) for bad arguments.
- Log with `slog.Info/Warn/Error("message", "key", value)` — no format strings, no `log.Fatal`.
- Access config via `config.Get()` — treat the pointer as read-only.

---

## Step 2 — Register in the dispatcher

Add a case in the `switch` block in [internal/commands/commands_server.go](internal/commands/commands_server.go):

```go
case strings.HasPrefix(lower, ".mycmd"):
    replies, err := ProcessMyCommand(text)
    return replies, "mycmd", err
```

Place it before the final `default` (which returns `nil, "", nil`). Use exact equality (`lower == ".mycmd"`) for commands that take no arguments.

---

## Step 3 — Add config fields (if needed)

Edit [internal/config/model.go](internal/config/model.go) and add a field with a `yaml:` tag:

```go
type Config struct {
    // ...
    MyCmd struct {
        SomeOption string `yaml:"some_option"`
    } `yaml:"mycmd"`
}
```

Access it anywhere with `config.Get().MyCmd.SomeOption`. Document new keys in `config.example.yaml`.

---

## Return value semantics

A handler returns `([]string, error)`; the dispatcher returns `([]string, string, error)` (the middle value is the command name).

**Handler return value:**

| Return value | Meaning |
|---|---|
| `[]string{"reply"}, nil` | Send reply to sender |
| `[]string{"a", "b"}, nil` | Send two separate messages |
| `nil, nil` | Nothing to send |
| `nil, err` | Log error, send nothing |

**Dispatcher return value** (what the matching `case` returns):

| Return value | Meaning |
|---|---|
| `replies, "mycmd", nil` | Send replies; `"mycmd"` selects the mesh-fit variant |
| `nil, "", nil` | No match — send nothing |
| `nil, "", err` | Log error, send nothing |

The bot prefixes channel replies with `@[SenderName]` automatically; DM replies are sent as-is.

---

## Using PingMeta

`pingMeta` is passed into `GenerateReplies` but not forwarded to handlers by default. If your command needs it (e.g., distance, hop path, GPS), add it to your function signature and pass it from the dispatcher:

```go
case strings.HasPrefix(lower, ".mycmd"):
    replies, err := ProcessMyCommand(text, pingMeta)
    return replies, "mycmd", err
```

```go
type PingMeta struct {
    PathLength int
    PathBytes  []byte
    Timestamp  time.Time
    SenderLat  *float64   // nil if unknown
    SenderLon  *float64
    SelfLat    float64
    SelfLon    float64
    HopCoords  [][2]float64
    HopKnown   []bool
}
```

---

## Reading message history

Use `historylog.ReadLastChannel` to fetch recent messages (as `.last` does):

```go
import "meshbot/internal/historylog"

entries, err := historylog.ReadLastChannel(baseDir, channelName, n)
// entries[i].Time, entries[i].Sender, entries[i].Text
```

`baseDir` is the package constant `historyBaseDir` (`"log/history"`) defined in [internal/commands/last.go](internal/commands/last.go); reuse it rather than re-deriving the path.

---

## Updating the help command

[internal/commands/help.go](internal/commands/help.go) handles `.help` and `.help <cmd>`. Add your command in two places:

**1. Add a help string constant:**

```go
const myCmdHelp = ".mycmd <arg>\nwhat it does"
```

**2. Add the command name to `helpList`** (the list shown by bare `.help`):

```go
const helpList = ".ping .trace .last .llm .news .w .path .2ch .claw .8ball .top .stat .dice .num .hang .mycmd\n\".help <cmd>\" for details"
```

**3. Add a case to `ProcessHelpCommand`:**

```go
case "mycmd":
    return myCmdHelp, nil
```

---

## Checklist

- [ ] `internal/commands/mycmd.go` — handler function(s)
- [ ] `commands_server.go` — new `case` in `switch`
- [ ] `config/model.go` + `config.example.yaml` — if config is needed
- [ ] Update the commands table in `CLAUDE.md`
- [ ] `help.go` — add constant, update `helpList`, add `case` in `ProcessHelpCommand`
- [ ] `go vet ./...` passes
- [ ] Manual test via `cmd/example`
