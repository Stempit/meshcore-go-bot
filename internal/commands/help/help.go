// Package help answers .help by rendering the command index and per-command
// help from the registry's registered Meta.
package help

import (
	"strings"

	"meshbot/internal/commands/registry"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:     "help",
		Triggers: []string{".help"},
		Match:    registry.Prefix,
		// HelpShort intentionally empty: .help is not listed in its own index.
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	arg := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(c.Text), ".help"))
	if arg == "" {
		return []string{registry.HelpIndex()}, nil
	}
	return []string{registry.HelpFor(arg)}, nil
}
