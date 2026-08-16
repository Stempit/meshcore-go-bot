# weather — current / next-day weather

Package `weather` registers the `.w` command: fetches current or tomorrow's conditions
from [wttr.in](https://wttr.in) (no API key) and returns a compact Russian line sized
for the mesh limit (≤130 bytes).

## Command surface

```
.w                        – current weather at the default location
.w <location>             – current weather at <location>
.w завтра                 – tomorrow's weather at the default location
.w завтра <location>      – tomorrow at <location>
.w tomorrow <location>    – same (English alias)
```

`<location>` is free-form (city name or coordinates) passed straight to wttr.in. The
`завтра`/`tomorrow` keyword is stripped before the location is extracted; if the
location is then empty, `default_location` is used.

Reply format: `<emoji> <Город>: <temp>°C, <условие>, 💨 <speed>м/с <dir>`, e.g.
`⛅ Москва: +12°C, переменная облачность, 💨 4м/с СВ`. Unknown location / provider
error → a short Russian error string.

- **Current**: wttr.in plain-text format endpoint (`?format=…&lang=ru`).
- **Tomorrow**: wttr.in JSON endpoint (`?format=j1`), reading `weather[1]`; the
  `weatherCode` is mapped to an emoji, unknown codes fall back to 🌡️.

HTTP goes through the shared `proxy.HTTPClient()` (honours the SOCKS5 `Proxy` config).
No caching — the mesh link is slow, so fresh data on every request is fine.

## Configuration

YAML key `Weather`. `.w` is routed regardless of `enabled` (the flag is currently
reserved/unused).

```yaml
Weather:
  enabled: true
  default_location: "Moscow"   # used when .w is called with no argument
  units: metric                # metric (°C, m/s) or imperial (°F, mph)
```

`units` maps to wttr.in's `?m` (metric) / `?u` (imperial) flag.
