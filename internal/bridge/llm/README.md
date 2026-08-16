# llm — LLM providers

Package `llm` is the provider layer for LLM queries: it exposes two functions that turn
a model + system prompt + temperature + request into a text response. It is used by the
[`.llm`](../../commands/llm/README.md) command and by the `llm` variant of
[`internal/shrink`](../../shrink/README.md).

## Providers

| Function | Provider | Notes |
|----------|----------|-------|
| `GetRemoteLLMResponse(model, systemPrompt, temperature, request)` | OpenRouter | via `github.com/revrost/go-openrouter`; HTTP through `internal/proxy` |
| `GetLocalLLMResponse(model, systemPrompt, temperature, request)` | Ollama | via `github.com/ollama/ollama/api` |

The caller selects which to use from the `LLM.type` config (`openrouter` / `ollama`).
This package holds no config of its own and does **not** import `internal/config` (which
keeps `internal/shrink` free of an import cycle).

## Environment

- `OPENROUTER_API_KEY` — required for OpenRouter.
- `OLLAMA_HOST` — Ollama base URL (default `http://localhost:11434`).

Model, system prompt, and temperature come from the caller (see the `LLM` and
`Shrink.llm` config sections).
