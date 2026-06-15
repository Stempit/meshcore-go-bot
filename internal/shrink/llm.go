package shrink

import (
	"log/slog"
	"strings"

	"meshbot/internal/config"
	"meshbot/internal/llm"
)

// llmShrink compresses text with the configured shrink model, retrying against
// the exact budget (max - head). It falls back to split() whenever it cannot
// deliver a short-enough result: LLM disabled, call error, unknown provider, or
// retries exhausted. split() always succeeds, so there is no give-up message.
func llmShrink(text string, max, head int) []string {
	cfg := config.Get()
	budget := max - head
	if budget < 1 {
		budget = 1
	}

	if !cfg.LLM.Enabled {
		return split(maybeCyrToLat(text), max, head)
	}

	sc := cfg.Shrink.LLM
	provider := sc.Type
	if provider == "" {
		provider = cfg.LLM.Type
	}
	model := sc.Model
	if model == "" {
		model = cfg.LLM.Model
	}
	retries := sc.MaxRetries
	if retries < 1 {
		retries = 1
	}

	resp := text
	for i := 0; i < retries; i++ {
		var (
			out string
			err error
		)
		switch provider {
		case "openrouter":
			out, err = llm.GetRemoteLLMResponse(model, sc.Prompt, sc.Temperature, resp)
		case "ollama":
			out, err = llm.GetLocalLLMResponse(model, sc.Prompt, sc.Temperature, resp)
		default:
			return split(maybeCyrToLat(text), max, head)
		}
		if err != nil {
			slog.Warn("shrink: llm error", "error", err)
			return split(maybeCyrToLat(text), max, head)
		}
		resp = strings.TrimSpace(out)
		// Apply the lossless squeeze to the LLM output (not its input) before
		// checking the budget: the model compresses clean Cyrillic, then the
		// homoglyph swap shaves the final bytes.
		if squeezed := maybeCyrToLat(resp); len(squeezed) <= budget {
			slog.Info("shrink: llm response", "length", len(squeezed), "try", i+1)
			return []string{squeezed}
		}
		slog.Info("shrink: llm response", "length", len(resp), "try", i+1)
	}
	return split(maybeCyrToLat(text), max, head)
}
