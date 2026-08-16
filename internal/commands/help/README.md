# help — command index

Package `help` registers the `.help` command: renders the grouped command index from
every plugin's registered `Meta` (`registry.HelpIndex()`). There is no hand-maintained
list — a command appears in the index when its `Meta` has a non-empty `HelpShort` and a
`Group`.

## Command surface

```
.help          – grouped index of all listed commands
.<cmd> help    – long help for one command (Meta.HelpLong)
```

Per-command help (`.<cmd> help`) is **not** handled here — it is intercepted centrally
in `registry.Dispatch` before the normal matchers, so it beats interceptors like claw
and bbs. `.help` itself is not listed in its own index.

## Configuration

None.
