// Package words answers .words — the "Слова из слова" anagram game: a timed
// round where players build words from one long base word's letters
// (score = len²). Opt-in via config; channels collaborative-competitive, DMs
// solo; 30-day leaderboard. The round timer pushes results via games.Announce.
package words

import (
	"meshbot/internal/registry"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "words",
		Triggers:  []string{".words"},
		Match:     registry.Prefix,
		HelpShort: "anagram game",
		HelpLong:  ".words <слово> — слова из слова\n.words start / status / end / top",
		Group:     "Games",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processWords(c.Text, c.SenderKey, c.SenderName, c.Channel)
}
