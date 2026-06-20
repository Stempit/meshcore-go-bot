// Package help answers .help by rendering the grouped command index from the
// registry's registered Meta. Per-command detail is reached via ".<cmd> help",
// handled centrally in registry.Dispatch.
package help

import (
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

func (plugin) Handle(registry.Ctx) ([]string, error) {
	return []string{registry.HelpIndex()}, nil
}
