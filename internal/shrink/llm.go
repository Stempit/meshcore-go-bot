package shrink

import (
	"strings"

	"meshbot/internal/bridge/llm"
	"meshbot/internal/config"
	"github.com/stempit/meshcore-companion/fit"
)

// llmCompressor implements the "llm" fit variant: it compresses text with the
// configured shrink model, retrying against the exact budget (max - head). It
// falls back to fit.Split whenever it cannot deliver a short-enough result:
// LLM disabled, call error, unknown provider, or retries exhausted. Split
// always succeeds, so there is no give-up message.
func llmCompressor(o fit.Options, text string, head int) []string {
	cfg := config.Get()
	budget := o.Budget(head)
	fallback := func() []string { return fit.SplitParts(o, text, head) }

	if !cfg.LLM.Enabled {
		return fallback()
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
			return fallback()
		}
		if err != nil {
			log().Warn("shrink: llm error", "error", err)
			return fallback()
		}
		resp = strings.TrimSpace(out)
		// Apply the lossless squeeze to the LLM output (not its input) before
		// checking the budget: the model compresses clean Cyrillic, then the
		// homoglyph swap shaves the final bytes.
		if squeezed := o.Squeeze(resp); len(squeezed) <= budget {
			log().Info("shrink: llm response", "length", len(squeezed), "try", i+1)
			return []string{squeezed}
		}
		log().Info("shrink: llm response", "length", len(resp), "try", i+1)
	}
	return fallback()
}
