// Package llm answers .llm <query> by querying the configured LLM provider.
package llm

import (
	"embed"
	"strings"

	provider "github.com/stempit/meshcore-go-bot/internal/bridge/llm"
	"github.com/stempit/meshcore-go-bot/internal/config"
	"github.com/stempit/meshcore-go-bot/internal/logging"
	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

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

// LocalizedHelp returns the per-language long help for ".llm help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	reply, err := ProcessLLMMessage(c.Text)
	if err != nil {
		return []string{tr.T(c.Lang, "error")}, nil
	}
	return []string{reply}, nil
}

// ProcessLLMMessage queries the configured LLM and returns the raw response.
// Mesh-fit shrinking is applied centrally at the send site (internal/shrink).
// Exported for the manual_test.go prompt harness (-tags manual). The
// bot-wide language (global config) steers the model's reply language via an
// appended system directive.
func ProcessLLMMessage(text string) (response string, err error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, ".llm"))

	cfg := config.Get()

	lang := i18n.Base
	if cfg.Language != "" {
		lang = cfg.Language
	}
	systemPrompt := cfg.LLM.SystemPrompt
	if d := tr.T(lang, "reply_directive"); d != "" && d != "reply_directive" {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n" + d)
	}

	switch cfg.LLM.Type {
	case "openrouter":
		response, err = provider.GetRemoteLLMResponse(cfg.LLM.Model, systemPrompt, cfg.LLM.SystemTemperature, text)
	case "ollama":
		response, err = provider.GetLocalLLMResponse(cfg.LLM.Model, systemPrompt, cfg.LLM.SystemTemperature, text)
	}

	if err != nil {
		log().Warn("Main LLM request error", "error", err)
		return
	}
	log().Info("Main LLM response", "response", response, "length", len(response))

	return response, nil
}
