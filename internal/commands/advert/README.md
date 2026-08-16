# advert — .advert parser

Package `advert` parses the `.advert` command. Unlike the registry command plugins,
`.advert` is **not** a self-registering plugin: it drives the MeshCore client's
`SendSelfAdvert`, which command handlers have no access to, so it is handled in the
[dispatch](../../dispatch/README.md) layer *before* `registry.Dispatch`.

This package therefore exposes only a parser that dispatch calls:

```go
flood, ok := advert.ParseAdvertCommand(text)
```

- `ok` — the text is a `.advert` command.
- `flood` — true unless the argument is `direct` (`.advert direct` sends a
  direct/non-flood advert; `.advert` alone floods).

## Configuration

None.
