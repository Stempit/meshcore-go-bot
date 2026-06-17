package commands

import (
	"fmt"
	"strings"
	"time"

	"meshbot/internal/historylog"
)

// statWindow is the trailing period summarised by ".stat".
const statWindow = 24 * time.Hour

// ProcessStatCommand reports traffic stats for the channel where ".stat" was
// called: message count over the last 24h, the busiest clock hour, and the
// number of unique authors. The bot's own ("Me") messages are excluded so the
// numbers reflect human activity. Channels only; in DMs the channel name is
// empty and it reports that the command is unavailable.
func ProcessStatCommand(currentChannel string) ([]string, error) {
	if strings.TrimSpace(currentChannel) == "" {
		return []string{"Статистика доступна только в каналах"}, nil
	}

	entries, err := historylog.ReadAllChannel(historyBaseDir, currentChannel)
	if err != nil {
		return nil, fmt.Errorf(".stat: %w", err)
	}

	cutoff := time.Now().Add(-statWindow)

	total := 0
	users := make(map[string]struct{})
	var hourCounts [24]int
	for _, e := range entries {
		if e.Sender == "Me" || e.Time.Before(cutoff) {
			continue
		}
		total++
		users[e.Sender] = struct{}{}
		hourCounts[e.Time.Hour()]++
	}

	if total == 0 {
		return []string{"📊 3a 24ч cooбщeний нeт"}, nil
	}

	// Busiest clock hour; ties resolve to the earliest hour.
	peakHour, peakCount := 0, 0
	for h, c := range hourCounts {
		if c > peakCount {
			peakHour, peakCount = h, c
		}
	}

	msg := fmt.Sprintf(
		"📊 Kaнaл зa 24ч:\n✉️ %d cooбщ\n👥 %d yчacт\n⏰ пик %02d:00 (%d)",
		total, len(users), peakHour, peakCount,
	)
	return []string{msg}, nil
}
