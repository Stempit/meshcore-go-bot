// Package eightball answers .8ball with a random Magic 8-Ball phrase.
package eightball

import (
	"embed"
	"math/rand"
	"time"

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
		Name:      "8ball",
		Triggers:  []string{".8ball"},
		Match:     registry.Prefix,
		HelpShort: "magic 8-ball",
		HelpLong:  ".8ball <question>\nthe magic ball answers yes/no", // English base; ru via LocalizedHelp
		Group:     "Other",
	}
}

// LocalizedHelp returns the per-language long help for ".8ball help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

// Handle answers a yes/no question with a random Magic 8-Ball phrase. The
// question text itself is ignored — like the classic toy.
func (plugin) Handle(c registry.Ctx) ([]string, error) {
	question := c.Arg(".8ball")
	if question == "" {
		return []string{tr.T(c.Lang, "usage")}, nil
	}
	answers := tr.TList(c.Lang, "answers")
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return []string{"🎱 " + answers[r.Intn(len(answers))]}, nil
}
