package alerts

// Config holds the alert poller's settings: a list of independent sources,
// each polled on its own schedule and routed to its own mesh channel.
type Config struct {
	Sources []SourceConfig `yaml:"sources"`
}

// SourceConfig describes one alert source to poll.
type SourceConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Name            string `yaml:"name"` // used for logging and the seen-ids file name, e.g. "bpla", "mchs"
	Type            string `yaml:"type"` // "html_cards" (bplarussia.ru-style incident cards) | "rss"
	URL             string `yaml:"url"`
	Channel         string `yaml:"channel"`           // MeshCore channel name, e.g. "#alerts" — must already exist on the device
	PollIntervalSec int    `yaml:"poll_interval_sec"` // default 60
	MaxPerPoll      int    `yaml:"max_per_poll"`      // default 3
	TitlePrefix     string `yaml:"title_prefix"`      // optional: skip items whose title doesn't start with this (e.g. "Внимание")
	Postfix         string `yaml:"postfix"`           // optional: appended as-is to the announced message text (include your own separator, e.g. " #bpla")
}
