package words

import (
	"meshbot/internal/config"
	"meshbot/internal/games"
)

// cfg is the .words config section (yaml key "words"). It is opt-in via Enabled
// (same convention as Claw).
type cfg struct {
	Enabled    bool   `yaml:"enabled"`      // opt-in: .words stays off unless true
	DictPath   string `yaml:"dict_path"`    // dictionary file, default "words_anagram_ru.txt"
	RoundSec   int    `yaml:"round_sec"`    // countdown length, default 300 (5 min)
	MinSubLen  int    `yaml:"min_sub_len"`  // shortest valid submission, default 3
	BaseMinLen int    `yaml:"base_min_len"` // shortest base word, default 8
	BaseMaxLen int    `yaml:"base_max_len"` // longest base word, default 10
}

var conf cfg

func (plugin) ConfigKey() string { return "words" }

// Configure decodes the section, installs the tunables into the games support
// package, and loads the runtime dictionary (best-effort: a missing file just
// disables the game with "Словарь недоступен", the rest of the bot runs).
func (plugin) Configure(s config.Sectioner) error {
	if err := s.Section("words", &conf); err != nil {
		return err
	}
	games.SetWordsSettings(games.WordsSettings{
		RoundSec:   conf.RoundSec,
		MinSubLen:  conf.MinSubLen,
		BaseMinLen: conf.BaseMinLen,
		BaseMaxLen: conf.BaseMaxLen,
	})
	_ = games.LoadWordsDict(conf.DictPath)
	return nil
}

func (plugin) Enabled() bool { return conf.Enabled }
