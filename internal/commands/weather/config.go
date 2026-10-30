package weather

import (
	"github.com/stempit/meshcore-go-bot/internal/config"
	"github.com/stempit/meshcore-go-bot/internal/logging"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

var log = logging.For("commands")

// cfg is the weather plugin's config section (yaml key "Weather").
type cfg struct {
	Enabled         bool   `yaml:"enabled"`
	DefaultLocation string `yaml:"default_location"`
	Units           string `yaml:"units"` // "metric" or "imperial"
}

var conf cfg

func (plugin) ConfigKey() string { return "Weather" }

func (plugin) Configure(s config.Sectioner) error {
	return s.Section("Weather", &conf)
}

// Enabled honors the Weather.enabled flag like the other opt-in plugins
// (words, vote, poker): false hides .w from routing and help. The example
// config ships enabled: true, which preserves the original always-on switch.
func (plugin) Enabled() bool { return conf.Enabled }

func init() { registry.Register(plugin{}) }
