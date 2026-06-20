// Package hang answers .hang via the games support package.
package hang

import (
	"meshbot/internal/commands/registry"
	"meshbot/internal/games"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "hang",
		Triggers:  []string{".hang"},
		Match:     registry.Prefix,
		HelpShort: "hangman",
		HelpLong:  ".hang <буква> — виселица\n.hang start / <слово> / status / end / top",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return games.ProcessHangCommand(c.Text, c.SenderKey, c.SenderName, c.Channel)
}
