package agent

import (
	"github.com/stempit/meshcore-go-bot/internal/bridge/agentclient"
	"github.com/stempit/meshcore-go-bot/internal/config"
	"github.com/stempit/meshcore-go-bot/internal/logging"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

var log = logging.For("commands")

// cfg is the agent plugin's config section (yaml key "Agent").
type cfg struct {
	Enabled    bool   `yaml:"enabled"`
	Type       string `yaml:"type"`        // openai (default) | picoclaw
	URL        string `yaml:"url"`         // openai: base URL; picoclaw: ws://host:18790/pico/ws
	Token      string `yaml:"token"`       // bearer token; empty = none
	Model      string `yaml:"model"`       // openai only; empty = server default
	TimeoutSec int    `yaml:"timeout_sec"` // default 180
	BotName    string `yaml:"bot_name"`    // mesh display name of the bot, e.g. "Yorick 💀"
}

var conf cfg

// client is built once in Configure; the Enabled gate in Dispatch means
// Handle never runs before that.
var client agentclient.Client

func (plugin) ConfigKey() string { return "Agent" }

func (plugin) Configure(s config.Sectioner) error {
	if err := s.Section("Agent", &conf); err != nil {
		return err
	}
	if !conf.Enabled {
		return nil
	}
	if conf.Type == "" {
		conf.Type = "openai"
	}
	// Build the client at startup so an unknown type or missing url fails
	// loudly via registry.ConfigureAll instead of at first use.
	cl, err := agentclient.New(agentclient.Config{
		Type:  conf.Type,
		URL:   conf.URL,
		Token: conf.Token,
		Model: conf.Model,
	})
	if err != nil {
		return err
	}
	client = cl
	return nil
}

// Enabled honors the Agent.enabled flag: false hides .agent and the @[BotName]
// mention matcher from routing and help (the example config ships enabled:
// false). When enabled but unconfigured, the handler still replies with the
// friendly not_configured message instead of vanishing.
func (plugin) Enabled() bool { return conf.Enabled }

func init() { registry.Register(plugin{}) }
