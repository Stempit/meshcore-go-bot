// Package words answers .words — the "Слова из слова" anagram game: a timed
// round where players build words from one long base word's letters
// (score = len²). Opt-in via config; channels collaborative-competitive, DMs
// solo; 30-day leaderboard. The round timer pushes results via games.Announce.
package words

import (
	"embed"

	"meshbot/internal/support/i18n"
	"meshbot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "words",
		Triggers:  []string{".words"},
		Match:     registry.Prefix,
		HelpShort: "anagram game",
		HelpLong:  ".words <word> — words from a word\n.words start / status / end / top",
		Group:     "Games",
	}
}

// LocalizedHelp returns the per-language long help for ".words help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processWords(c.Lang, c.Text, c.SenderKey, c.SenderName, c.Channel)
}
