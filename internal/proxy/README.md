# proxy — shared HTTP client with optional SOCKS5

Package `proxy` provides a shared `*http.Client` with optional SOCKS5 proxy support, so
every outbound HTTP caller (LLM, Telegram, weather) routes through one place.

## API

```go
proxy.Init(rawURL)          // configure once at startup from the Proxy config
client := proxy.HTTPClient() // the shared client (proxied when enabled)
```

`Init` takes a `socks5://[user:pass@]host:port` URL. When the proxy is disabled the
shared client is a plain 30s-timeout `http.Client`; when enabled, its transport dials
through the SOCKS5 dialer. Callers use `proxy.HTTPClient()` and add their own
per-request `context` timeout.

## Configuration

YAML key `Proxy` (infra config).

```yaml
Proxy:
  enabled: false
  url: "socks5://user:pass@host:1080"
```
