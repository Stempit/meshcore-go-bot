# allcommands — plugin aggregator

Package `allcommands` blank-imports every command plugin purely for its registration
side effect (each plugin's `init()` calls `registry.Register`). `cmd/bot` blank-imports
this one package so that all commands self-register at startup.

```go
import _ "github.com/stempit/meshcore-go-bot/internal/commands/allcommands"
```

## Adding a command

Create `internal/commands/<name>/` implementing `registry.Command`, then add one blank
import line here. That is the only edit outside the new command's own folder. See
[docs/NEW_COMMAND.md](../../../docs/NEW_COMMAND.md) and the framework in
[`internal/support/registry`](../../support/registry/README.md).

> Note: `.help` lists commands in registration order, which the Go runtime fixes as
> lexicographic import-path order — so the index is alphabetical by trigger regardless
> of the order of imports in this file.

## Configuration

None.
