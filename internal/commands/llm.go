package commands

import (
	"log/slog"
	"meshbot/internal/config"
	"meshbot/internal/llm"
	"strings"
)

// ProcessLLMMessage queries the configured LLM and returns the raw response.
// Mesh-fit shrinking is applied centrally at the send site (internal/shrink).
func ProcessLLMMessage(text string) (response string, err error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, ".llm"))

	cfg := config.Get()

	switch cfg.LLM.Type {
	case "openrouter":
		response, err = llm.GetRemoteLLMResponse(cfg.LLM.Model, cfg.LLM.SystemPrompt, cfg.LLM.SystemTemperature, text)
	case "ollama":
		response, err = llm.GetLocalLLMResponse(cfg.LLM.Model, cfg.LLM.SystemPrompt, cfg.LLM.SystemTemperature, text)
	}

	if err != nil {
		slog.Warn("Main LLM request error", "error", err)
		return
	}
	slog.Info("Main LLM response", "response", response, "length", len(response))

	return response, nil
}
