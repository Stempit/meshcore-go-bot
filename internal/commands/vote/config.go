package vote

import "github.com/stempit/meshcore-go-bot/internal/config"

// cfg is the .vote config section (yaml key "vote"). It is opt-in via Enabled
// (same convention as .words). The int fields are defaulted at their use sites
// when zero (see voteRoundDur/voteMaxOptions in poll.go).
type cfg struct {
	Enabled    bool `yaml:"enabled"`     // opt-in: .vote stays off unless true
	RoundSec   int  `yaml:"round_sec"`   // auto-close countdown, default 180 (3 min)
	MaxOptions int  `yaml:"max_options"` // max options per poll, default 6
}

var conf cfg

func (plugin) ConfigKey() string { return "vote" }

func (plugin) Configure(s config.Sectioner) error {
	if err := s.Section("vote", &conf); err != nil {
		return err
	}
	return nil
}

func (plugin) Enabled() bool { return conf.Enabled }
