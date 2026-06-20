// Package words answers .words (the "Слова из слова" anagram game) via the
// games support package. It owns the game's config section and is opt-in.
package words

import (
	"meshbot/internal/commands/registry"
	"meshbot/internal/games"
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
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return games.ProcessWordsCommand(c.Text, c.SenderKey, c.SenderName, c.Channel)
}
