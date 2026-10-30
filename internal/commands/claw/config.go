package claw

import (
	"github.com/stempit/meshcore-go-bot/internal/config"
	"github.com/stempit/meshcore-go-bot/internal/logging"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

var log = logging.For("commands")

// cfg is the claw plugin's config section (yaml key "Claw").
type cfg struct {
	Enabled    bool   `yaml:"enabled"`
	URL        string `yaml:"url"`         // ws://host:18790/pico/ws (gateway port, not dashboard)
	Token      string `yaml:"token"`       // from picoclaw dashboard → Pico token
	TimeoutSec int    `yaml:"timeout_sec"` // default 180
	BotName    string `yaml:"bot_name"`    // mesh display name of the bot, e.g. "Yorick 💀"
}

var conf cfg

func (plugin) ConfigKey() string { return "Claw" }

func (plugin) Configure(s config.Sectioner) error {
	return s.Section("Claw", &conf)
}

// Enabled honors the Claw.enabled flag: false hides .claw and the @[BotName]
// mention matcher from routing and help (the example config ships enabled:
// false). When enabled but unconfigured, the handler still replies with the
// friendly not_configured message instead of vanishing.
func (plugin) Enabled() bool { return conf.Enabled }

func init() { registry.Register(plugin{}) }
