// Package dice answers .dice via the games support package.
package dice

import (
	"meshbot/internal/commands/registry"
	"meshbot/internal/games"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "dice",
		Triggers:  []string{".dice"},
		Match:     registry.Prefix,
		HelpShort: "roll dice",
		HelpLong:  ".dice [NdM] — кинуть кости (def 1d6)\n.dice start [NdM] / roll / end — раунд в канале",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return games.ProcessDiceCommand(c.Text, c.SenderName, c.Channel)
}
