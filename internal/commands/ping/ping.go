// Package ping answers .ping/ping/пинг with a random pong string.
package ping

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
		Name:      "ping",
		Triggers:  []string{".ping", "ping", "пинг"},
		Match:     registry.Exact,
		HelpShort: "random pong",
		HelpLong:  ".ping — random pong\nalso: ping, пинг", // English base; ru via LocalizedHelp
		Group:     "Service",
	}
}

// LocalizedHelp returns the per-language long help for ".ping help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	pool := tr.TList(c.Lang, "pool")
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return []string{pool[r.Intn(len(pool))]}, nil
}
