package path

import (
	"meshbot/internal/commands/registry"
	"meshbot/internal/config"
	"meshbot/internal/logging"
)

var log = logging.For("commands")

// cfg is the path plugin's config section (yaml key "Stats"): the Let's Mesh
// analyzer edge-stats data source it reads. Note this is the stats.json source
// owned by .path, not the unrelated .stat channel-traffic command.
type cfg struct {
	URL         string `yaml:"url"`           // HTTP(S) URL or file:///path/to/stats.json
	CacheTTLSec int    `yaml:"cache_ttl_sec"` // default 300
}

var conf cfg

func (plugin) ConfigKey() string { return "Stats" }

func (plugin) Configure(s config.Sectioner) error {
	return s.Section("Stats", &conf)
}

func (plugin) Enabled() bool { return true }

func init() { registry.Register(plugin{}) }
