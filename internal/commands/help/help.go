// Package help answers .help by rendering the grouped command index from the
// registry's registered Meta. Per-command detail is reached via ".<cmd> help",
// handled centrally in registry.Dispatch.
package help

import (
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
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
	return []string{registry.HelpIndex(c.Lang)}, nil
}
