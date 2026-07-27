package alerts

// Config holds the FPV/UAV alert-site poller's settings.
type Config struct {
	Enabled         bool   `yaml:"enabled"`
	URL             string `yaml:"url"`
	Channel         string `yaml:"channel"`           // MeshCore channel name, e.g. "#alerts" — must already exist on the device
	PollIntervalSec int    `yaml:"poll_interval_sec"` // default 60
	MaxPerPoll      int    `yaml:"max_per_poll"`      // default 3
}
