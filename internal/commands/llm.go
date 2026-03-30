package commands

import (
	"log/slog"
	"meshbot/internal/config"
	"meshbot/internal/llm"
	"strings"
)

func ProcessLLMMessage(text string) (response string, err error) {
	text = strings.ReplaceAll(text, ".llm", "")

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

	return ShrinkText(response)
}

// ShrinkText uses the LLM shrink prompt to compress text to under 130 characters.
// Returns the original text unchanged if LLM is disabled or shrinking is not needed.
func ShrinkText(text string) (string, error) {
	if len(text) <= 130 {
		return text, nil
	}

	cfg := config.Get()
	if !cfg.LLM.Enabled {
		return text, nil
	}

	var (
		response = text
		err      error
	)
	for i := 1; i <= cfg.LLM.ShrinkMaxRetries; i++ {
		switch cfg.LLM.Type {
		case "openrouter":
			response, err = llm.GetRemoteLLMResponse(cfg.LLM.Model, cfg.LLM.ShrinkPrompt, cfg.LLM.ShrinkTemperature, response)
		case "ollama":
			response, err = llm.GetLocalLLMResponse(cfg.LLM.Model, cfg.LLM.ShrinkPrompt, cfg.LLM.ShrinkTemperature, response)
		}
		if err != nil {
			slog.Warn("Shrink LLM error", "error", err)
			return text, err
		}
		slog.Info("Shrink LLM response", "response", response, "length", len(response), "tries", i)
		if len(response) <= 130 {
			return response, nil
		}
	}
	return response, nil
}
