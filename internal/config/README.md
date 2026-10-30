# config — YAML config loader

Package `config` loads the single `config.yaml` once at startup (singleton via
`sync.Once`) and exposes it two ways:

- **Infra config** via `config.Get()` — a `*Config` treated as **read-only**, holding
  the cross-cutting sections: `Connection`, `Proxy`, `MQTT`, `Telegram`, `Alerts`,
  `Trace`, `Security`, `Redirect`, `Migration`, `Shrink`, `LLM`.
- **Per-plugin sections** via `Sectioner` — each command plugin owns its own YAML
  section and decodes it itself.

Restart to pick up config changes.

## Two-pass YAML (per-plugin config)

`Load` unmarshals the document once into both `Config` (infra) **and** a
`map[string]yaml.Node` of raw top-level sections. A plugin implementing
`registry.Configurable` then decodes its own section on demand:

```go
func (plugin) ConfigKey() string { return "words" }
func (plugin) Configure(s config.Sectioner) error {
    return s.Section("words", &conf)   // missing section → defaults stand, no error
}
```

`registry.ConfigureAll` runs every plugin's `Configure` once at startup (after `Load`),
so each plugin's `conf` is configured-once then read-only. Defaults live in the plugin's
`Configure` (applied when a value is zero), not here.

This keeps the user-facing config a **single file** while moving Go ownership of each
section next to its command. Infra vs per-command tiering: infra stays in `Config`;
per-command sections (`Weather`, `Top`, `Words`, `Agent`, `Stats`, `vote`) live in the
plugin folders. `Trace` stays central even though `.trace` is a command, because
dispatch reads it while building `PingMeta` before it knows which command will run.

## Files

| File | Role |
|------|------|
| `config.go` | `Load`, the `sync.Once` singleton, `Sectioner`, raw-node map |
| `model.go` | the infra `Config` struct + its section types |
| `helpers.go` | small accessors |

## Notes

- A misspelled section **key** fails silently (yaml.v3 ignores unknown keys), so the
  plugin runs on defaults — check for typos when adding a field (a YAML tag is required).
- The LLM shrink-retries key is `shrink_max_retries` (fixed from the original typo).
