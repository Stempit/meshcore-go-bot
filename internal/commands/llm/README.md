# llm — ask the LLM

Package `llm` registers the `.llm` command: sends the prompt to the configured LLM
provider and returns the raw answer. Mesh-fit shrinking (auto-compress to ≤130 chars) is
applied centrally at the send site by [`internal/shrink`](../../shrink/README.md), not
here.

## Command surface

```
.llm <query>   – ask the LLM
```

On any provider error the reply is `хз`. `ProcessLLMMessage` is exported so the
manual prompt harness can reuse it: `manual_test.go` (build tag `manual`),
run with `go test -tags manual ./internal/commands/llm -v`.

## Configuration

Uses the central `LLM` infra config (read via `config.Get()`), shared with the shrink
LLM variant. Provider is selected by `type` (`openrouter` or `ollama`).

```yaml
LLM:
  enabled: true
  type: openrouter          # openrouter | ollama
  model: <model-id>
  system_prompt: "..."
  system_temperature: 0.7
```

Provider plumbing lives in [`internal/bridge/llm`](../../bridge/llm/README.md):
`OPENROUTER_API_KEY` is required for OpenRouter; `OLLAMA_HOST` sets the Ollama base URL.
