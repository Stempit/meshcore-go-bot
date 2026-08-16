# logging — centralized logger factory

Package `logging` is a tiny factory for per-module `slog` loggers, so every package tags
its logs with a consistent `module` key.

## Usage

Assign the getter to a package-level var and call it at log sites:

```go
var log = logging.For("commands")

log().Info("started", "key", value)
```

`For(module)` returns a **lazy** getter (`sync.OnceValue`) that captures
`slog.Default()` on first use — i.e. after `main` has called `slog.SetDefault`, so
package-level `var log = logging.For(...)` initializers are safe.

## Conventions (project-wide)

- All log messages in **English**, with structured key-value pairs.
- No `log.Fatal` inside packages — return errors; only `os.Exit` in `main`.

## Configuration

None.
