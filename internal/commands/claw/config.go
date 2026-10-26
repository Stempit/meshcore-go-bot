package claw

import (
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
	"github.com/stempit/meshcore-go-bot/internal/config"
	"github.com/stempit/meshcore-go-bot/internal/logging"
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

// Enabled always reports true so the .claw trigger is routed even when picoclaw
// is unconfigured (the handler then replies "picoclaw не настроен"). The
// @[BotName] mention matcher applies its own Enabled/BotName gate.
func (plugin) Enabled() bool { return true }

func init() { registry.Register(plugin{}) }
