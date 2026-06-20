// Package llm answers .llm <query> by querying the configured LLM provider.
package llm

import (
	"strings"

	"meshbot/internal/registry"
	"meshbot/internal/config"
	provider "meshbot/internal/llm"
	"meshbot/internal/logging"
)

var log = logging.For("commands")

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "llm",
		Triggers:  []string{".llm"},
		Match:     registry.Prefix,
		HelpShort: "ask AI",
		HelpLong:  ".llm <query>\nask AI, auto-shrunk to <130 chars",
		Group:     "AI",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	reply, err := ProcessLLMMessage(c.Text)
	if err != nil {
		return []string{"хз"}, nil
	}
	return []string{reply}, nil
}

// ProcessLLMMessage queries the configured LLM and returns the raw response.
// Mesh-fit shrinking is applied centrally at the send site (internal/shrink).
// Exported for the cmd/llm prompt-test utility.
func ProcessLLMMessage(text string) (response string, err error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, ".llm"))

	cfg := config.Get()

	switch cfg.LLM.Type {
	case "openrouter":
		response, err = provider.GetRemoteLLMResponse(cfg.LLM.Model, cfg.LLM.SystemPrompt, cfg.LLM.SystemTemperature, text)
	case "ollama":
		response, err = provider.GetLocalLLMResponse(cfg.LLM.Model, cfg.LLM.SystemPrompt, cfg.LLM.SystemTemperature, text)
	}

	if err != nil {
		log().Warn("Main LLM request error", "error", err)
		return
	}
	log().Info("Main LLM response", "response", response, "length", len(response))

	return response, nil
}
