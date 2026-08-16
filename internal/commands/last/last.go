// Package last answers .last [n] [channel] with recent channel history.
package last

import (
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"meshbot/internal/support/helpers"
	"meshbot/internal/support/historylog"
	"meshbot/internal/support/i18n"
	"meshbot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

const (
	defaultLastCount = 3
	maxLastCount     = 7
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "last",
		Triggers:  []string{".last"},
		Match:     registry.Prefix,
		HelpShort: "last n msgs",
		HelpLong:  ".last [n] [channel]\nshow last n msgs (def 3, max 7) in channel",
		Group:     "Info",
	}
}

// LocalizedHelp returns the per-language long help for ".last help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	count, srcChannel, err := parseLastArgs(c.Lang, c.Text, c.Channel)
	if err != nil {
		return []string{err.Error()}, nil
	}

	entries, err := historylog.ReadLastChannel(helpers.HistoryDir, srcChannel, count)
	if err != nil {
		return nil, fmt.Errorf(".last: %w", err)
	}
	if len(entries) == 0 {
		return []string{tr.T(c.Lang, "no_history", srcChannel)}, nil
	}

	msgs := make([]string, len(entries))
	for i, e := range entries {
		msgs[i] = fmt.Sprintf("%s %s: %s", e.Time.Format("15:04"), e.Sender, e.Text)
	}
	return msgs, nil
}

func parseLastArgs(lang, text, currentChannel string) (int, string, error) {
	rest := strings.TrimSpace(text[len(".last"):])

	if rest == "" {
		return defaultLastCount, currentChannel, nil
	}

	parts := strings.Fields(rest)

	count, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", errors.New(tr.T(lang, "usage"))
	}
	if count < 1 {
		count = 1
	}
	if count > maxLastCount {
		count = maxLastCount
	}

	channel := currentChannel
	if len(parts) >= 2 {
		channel = parts[1]
	}

	return count, channel, nil
}
