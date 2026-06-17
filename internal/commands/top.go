package commands

import (
	"fmt"
	"sort"
	"strings"

	"meshbot/internal/config"
	"meshbot/internal/historylog"
)

const (
	defaultTopScanCount = 100 // recent messages scanned when Top.scan_count is unset
	topAuthorsShown     = 3   // how many ranked authors to return
	topNameMaxRunes     = 12  // author names are clipped to fit the 140-byte mesh limit
)

// topMedals decorates the first three places; lower places fall back to a number.
var topMedals = []string{"🥇", "🥈", "🥉"}

// ProcessTopCommand ranks the most active authors in the channel where ".top"
// was called. It scans the last N channel messages (N from Top.scan_count,
// default 100), excludes the bot's own ("Me") messages, and returns the top
// authors by message count. Only works in public channels; in DMs the channel
// name is empty and it reports that the command is unavailable.
func ProcessTopCommand(currentChannel string) ([]string, error) {
	if strings.TrimSpace(currentChannel) == "" {
		return []string{"Топ доступен только в каналах"}, nil
	}

	entries, err := historylog.ReadAllChannel(historyBaseDir, currentChannel)
	if err != nil {
		return nil, fmt.Errorf(".top: %w", err)
	}

	scanCount := config.Get().Top.ScanCount
	if scanCount <= 0 {
		scanCount = defaultTopScanCount
	}

	// Drop the bot's own messages, then keep only the most recent scanCount.
	var msgs []historylog.Entry
	for _, e := range entries {
		if e.Sender == "Me" {
			continue
		}
		msgs = append(msgs, e)
	}
	if len(msgs) > scanCount {
		msgs = msgs[len(msgs)-scanCount:]
	}
	if len(msgs) == 0 {
		return []string{"Пока нет сообщений для топа"}, nil
	}

	// Tally messages per author, preserving first-seen order for stable ties.
	counts := make(map[string]int)
	var order []string
	for _, e := range msgs {
		if _, seen := counts[e.Sender]; !seen {
			order = append(order, e.Sender)
		}
		counts[e.Sender]++
	}

	ranked := order
	sort.SliceStable(ranked, func(i, j int) bool {
		return counts[ranked[i]] > counts[ranked[j]]
	})
	if len(ranked) > topAuthorsShown {
		ranked = ranked[:topAuthorsShown]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🏆 Топ за %d сообщ:", len(msgs))
	for i, name := range ranked {
		var place string
		if i < len(topMedals) {
			place = topMedals[i]
		} else {
			place = fmt.Sprintf("%d", i+1)
		}
		// Count in brackets leads each line; the variable-length name trails.
		fmt.Fprintf(&b, "\n%s [%d] %s", place, counts[name], clipName(name))
	}
	return []string{b.String()}, nil
}

// clipName shortens an author name to topNameMaxRunes runes (adding an ellipsis)
// so a five-line top still fits the 140-byte mesh message limit.
func clipName(name string) string {
	r := []rune(name)
	if len(r) <= topNameMaxRunes {
		return name
	}
	return string(r[:topNameMaxRunes]) + "…"
}
