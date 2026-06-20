package weather

import (
	"meshbot/internal/registry"
	"meshbot/internal/config"
	"meshbot/internal/logging"
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

// Enabled always reports true: like the original switch, .w is routed
// regardless of the Weather.enabled flag (the flag is reserved/unused).
func (plugin) Enabled() bool { return true }

func init() { registry.Register(plugin{}) }
