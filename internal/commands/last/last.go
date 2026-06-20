// Package last answers .last [n] [channel] with recent channel history.
package last

import (
	"fmt"
	"strconv"
	"strings"

	"meshbot/internal/support/registry"
	"meshbot/internal/support/helpers"
	"meshbot/internal/support/historylog"
)

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

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	count, srcChannel, err := parseLastArgs(c.Text, c.Channel)
	if err != nil {
		return []string{err.Error()}, nil
	}

	entries, err := historylog.ReadLastChannel(helpers.HistoryDir, srcChannel, count)
	if err != nil {
		return nil, fmt.Errorf(".last: %w", err)
	}
	if len(entries) == 0 {
		return []string{fmt.Sprintf("No history for channel %q", srcChannel)}, nil
	}

	msgs := make([]string, len(entries))
	for i, e := range entries {
		msgs[i] = fmt.Sprintf("%s %s: %s", e.Time.Format("15:04"), e.Sender, e.Text)
	}
	return msgs, nil
}

func parseLastArgs(text, currentChannel string) (int, string, error) {
	rest := strings.TrimSpace(text[len(".last"):])

	if rest == "" {
		return defaultLastCount, currentChannel, nil
	}

	parts := strings.Fields(rest)

	count, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", fmt.Errorf("usage: .last [count] [channel]")
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
