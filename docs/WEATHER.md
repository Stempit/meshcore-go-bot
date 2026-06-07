# Weather command

## Overview

Add a `.w` command that fetches current or tomorrow's weather conditions and returns a compact response in Russian, suitable for mesh radio (≤130 bytes).

## Command syntax

```
.w                        – current weather at the default location
.w <location>             – current weather at <location>
.w завтра                 – tomorrow's weather at the default location
.w завтра <location>      – tomorrow's weather at <location>
.w tomorrow <location>    – same (English alias)
```

`<location>` is a free-form string (city name, coordinates, etc.) passed directly to the weather provider. The `завтра`/`tomorrow` keyword is stripped before the location is extracted.

## Response format

Single message, ≤130 bytes, in Russian, with emojis:

```
<emoji> <Город>: <temp>°C, <условие>, 💨 <speed>м/с <dir>
```

Examples:
```
⛅ Москва: +12°C, переменная облачность, 💨 4м/с СВ
❄️ Новосибирск: -3°C, снег, 💨 9м/с ЮЗ
☀️ Сочи: +24°C, ясно, 💨 2м/с З
```

For tomorrow requests the format is identical — the date is not shown in the reply.

If location is unknown or the provider returns an error, reply with a short Russian error string, e.g. `погода: город "foobar" не найден` or `погода: сервис недоступен`.

## Provider

Use [wttr.in](https://wttr.in) — no API key required, supports city names and coordinates out of the box.

### Current weather

Plain-text format endpoint: `/{location}?format=%c+%l:+%t,+%C,+💨+%w&lang=ru`

- `%c` – condition emoji (e.g. ⛅, ☀️, 🌧, ❄️) returned by wttr.in
- `%l` – location name as resolved by wttr.in (Russian when `lang=ru`)
- `%t` – temperature (with sign)
- `%C` – weather condition description in Russian
- `%w` – wind speed + direction

### Tomorrow weather

JSON endpoint: `/{location}?format=j1`

The response contains a `weather` array; index `0` is today, index `1` is tomorrow. Extract from `weather[1]`:

| JSON field | Usage |
|---|---|
| `weatherDesc[0].value` | condition text (English — translate via condition code or accept as-is) |
| `weatherCode` | map to emoji (see table below) |
| `avgtempC` / `avgtempF` | average temperature |
| `hourly[4].windspeed16Point` | midday wind speed |
| `hourly[4].winddir16Point` | midday wind direction |

**Condition code → emoji mapping** (subset, covers common cases):

| Codes | Emoji |
|---|---|
| 113 | ☀️ |
| 116 | ⛅ |
| 119, 122 | ☁️ |
| 143, 248, 260 | 🌫️ |
| 176, 263, 266, 293, 296, 299, 302, 305, 308, 353, 356, 359 | 🌧️ |
| 179, 182, 185, 281, 284, 311, 314, 317, 320, 362, 365, 374, 377 | 🌨️ |
| 200, 386, 389, 392, 395 | ⛈️ |
| 227, 230, 323, 326, 329, 332, 335, 338, 350, 368, 371 | ❄️ |

Unknown codes fall back to 🌡️.

If the response exceeds 130 bytes after trimming, pass it through `ShrinkText` (existing LLM shrink path).

## Configuration

New `Weather` block in `config.yaml` / `config.example.yaml`:

```yaml
Weather:
  enabled: true
  default_location: "Moscow"  # used when .w is called with no argument
  units: metric               # metric (°C, m/s) or imperial (°F, mph)
```

`units` maps to the wttr.in `?m` (metric) or `?u` (US/imperial) query flag.

## Implementation plan

1. **`internal/config/model.go`** – add `Weather WeatherConfig` field with `yaml:"Weather"` tag.
   ```go
   type WeatherConfig struct {
       Enabled         bool   `yaml:"enabled"`
       DefaultLocation string `yaml:"default_location"`
       Units           string `yaml:"units"` // "metric" or "imperial"
   }
   ```

2. **`internal/commands/weather.go`** – new file:
   - `ProcessWeatherCommand(text string) ([]string, error)`
   - Strip `.w` prefix, trim space.
   - Detect `завтра`/`tomorrow` keyword; set `day=1`, strip the keyword from the remaining text.
   - If location is empty after stripping, use `cfg.Weather.DefaultLocation`.
   - For `day=0`: call plain-text wttr.in endpoint, return trimmed response.
   - For `day=1`: call JSON endpoint, unmarshal `weather[1]`, build reply string from temp + condition + wind.
   - Use `proxy.HTTPClient()` (from `internal/proxy`) with a 5 s timeout via `context.WithTimeout`.
   - Call `ShrinkText` if reply exceeds 130 bytes.
   - Return `[]string{reply}`.

3. **`internal/commands/commands_server.go`** – add case to `GenerateReplies`:
   ```go
   case strings.HasPrefix(lower, ".w"):
       return ProcessWeatherCommand(text)
   ```

4. **`config.example.yaml`** – add the `Weather` block shown above.

## Proxy

The HTTP request to wttr.in must go through the SOCKS5 proxy when `Proxy.enabled: true` in config. Use the shared `proxy.HTTPClient()` from `internal/proxy` — it is already pre-configured with the dialer by `proxy.Init()` at startup (same as LLM/Telegram). No extra setup needed in the weather handler.

## Constraints

- No caching for the initial implementation — the mesh link is slow, so fresh data on every request is fine.
- Log the raw provider response at `slog.Debug` level and the final reply at `slog.Info`.
- Follow project conventions: `log/slog`, no `log.Fatal`, errors returned to caller.
