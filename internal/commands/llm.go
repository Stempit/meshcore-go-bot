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

	// need to shrink
	if len(response) >= 130 {

		// X shrink iterations
		for i := 1; i <= cfg.LLM.ShrinkMaxRetries; i++ {

			switch cfg.LLM.Type {
			case "openrouter":
				response, err = llm.GetRemoteLLMResponse(cfg.LLM.Model, cfg.LLM.ShrinkPrompt, cfg.LLM.ShrinkTemperature, response)
			case "ollama":
				response, err = llm.GetLocalLLMResponse(cfg.LLM.Model, cfg.LLM.ShrinkPrompt, cfg.LLM.ShrinkTemperature, response)
			}

			if err != nil {
				slog.Warn("Main LLM request error", "error", err)
				return
			}
			slog.Info("Shrink LLM response", "response", response, "length", len(response), "tries", i)

			if len(response) <= 130 {
				return
			}
		}
	}
	return
}
