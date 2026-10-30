// Package agentclient connects the bot to a remote AI agent over a pluggable
// transport. Config.Type selects the adapter: "openai" speaks an
// OpenAI-compatible chat completions API (nanobot, LiteLLM, vLLM, llama.cpp,
// …), "picoclaw" speaks the legacy Pico Protocol WebSocket. History is
// managed by the agent server; the client only carries a stable session ID.
package agentclient

import (
	"context"
	"fmt"

	"github.com/stempit/meshcore-go-bot/internal/logging"
)

var log = logging.For("agentclient")

// Client asks the agent to answer one prompt within a session and returns the
// agent's visible reply parts, in order. A part is one logical agent message —
// typically one item, but multi-message turns are preserved.
//
// sessionID should be stable per-scope (e.g. per channel or per DM sender) so
// the server keeps conversation history separate. The caller bounds each call
// with ctx.
type Client interface {
	Ask(ctx context.Context, sessionID, text string) ([]string, error)
}

// Config is the transport-agnostic adapter configuration.
type Config struct {
	Type  string `yaml:"type"`  // "openai" (default) | "picoclaw"
	URL   string `yaml:"url"`   // openai: base URL, e.g. http://127.0.0.1:8900; picoclaw: ws://host:18790/pico/ws
	Token string `yaml:"token"` // bearer token; empty = none
	Model string `yaml:"model"` // openai only; empty = server default
}

// New builds the Client selected by cfg.Type. An empty type means openai.
func New(cfg Config) (Client, error) {
	switch cfg.Type {
	case "", "openai":
		return newOpenAI(cfg)
	case "picoclaw":
		return newPicoClaw(cfg)
	default:
		return nil, fmt.Errorf("agentclient: unknown type %q (want \"openai\" or \"picoclaw\")", cfg.Type)
	}
}
