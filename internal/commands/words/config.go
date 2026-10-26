package words

import "github.com/stempit/meshcore-go-bot/internal/config"

// cfg is the .words config section (yaml key "words"). It is opt-in via Enabled
// (same convention as Claw). The int fields are defaulted at their use sites
// when zero (see the wordsRoundDur/wordsMinSub/... accessors in anagram.go).
type cfg struct {
	Enabled    bool `yaml:"enabled"`      // opt-in: .words stays off unless true
	RoundSec   int  `yaml:"round_sec"`    // countdown length, default 300 (5 min)
	MinSubLen  int  `yaml:"min_sub_len"`  // shortest valid submission, default 3
	BaseMinLen int  `yaml:"base_min_len"` // shortest base word, default 8
	BaseMaxLen int  `yaml:"base_max_len"` // longest base word, default 10
}

var conf cfg

func (plugin) ConfigKey() string { return "words" }

// Configure decodes the section and loads the embedded dictionary. It runs
// once at startup, before any round.
func (plugin) Configure(s config.Sectioner) error {
	if err := s.Section("words", &conf); err != nil {
		return err
	}
	loadWordsDict()
	return nil
}

func (plugin) Enabled() bool { return conf.Enabled }
