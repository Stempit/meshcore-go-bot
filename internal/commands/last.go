package commands

import (
	"fmt"
	"strconv"
	"strings"

	"meshbot/internal/historylog"
)

const (
	defaultLastCount = 3
	maxLastCount     = 7
	historyBaseDir   = "log/history"
)

// ProcessLastCommand parses ".last [n] [channel]" and returns formatted messages
// ready to be relayed into the current channel.
func ProcessLastCommand(text, currentChannel string) ([]string, error) {
	count, srcChannel, err := parseLastArgs(text, currentChannel)
	if err != nil {
		return []string{err.Error()}, nil
	}

	entries, err := historylog.ReadLastChannel(historyBaseDir, srcChannel, count)
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
