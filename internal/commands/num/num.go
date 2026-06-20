// Package num answers .num via the games support package.
package num

import (
	"meshbot/internal/commands/registry"
	"meshbot/internal/games"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "num",
		Triggers:  []string{".num"},
		Match:     registry.Prefix,
		HelpShort: "guess the number",
		HelpLong:  ".num <число> — угадай число (1–100)\n.num start [max] / status / end / top",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return games.ProcessNumCommand(c.Text, c.SenderKey, c.SenderName, c.Channel)
}
