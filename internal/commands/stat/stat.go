// Package stat answers .stat with channel traffic stats over the last 24h.
package stat

import (
	"embed"
	"fmt"
	"time"

	"github.com/stempit/meshcore-go-bot/internal/support/helpers"
	"github.com/stempit/meshcore-go-bot/internal/support/historylog"
	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

// statWindow is the trailing period summarised by ".stat".
const statWindow = 24 * time.Hour

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "stat",
		Triggers:  []string{".stat"},
		Match:     registry.Exact,
		HelpShort: "channel traffic",
		HelpLong:  ".stat — channel traffic over 24h: messages, peak, participants",
		Group:     "Info",
	}
}

// LocalizedHelp returns the per-language long help for ".stat help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

// Handle reports traffic stats for the channel where ".stat" was called:
// message count over the last 24h, the busiest clock hour, and the number of
// unique authors. The bot's own ("Me") messages are excluded. Channels only;
// in DMs it reports unavailable.
func (plugin) Handle(c registry.Ctx) ([]string, error) {
	if c.IsDM() {
		return []string{tr.T(c.Lang, "channels_only")}, nil
	}

	entries, err := historylog.ReadAllChannel(helpers.HistoryDir, c.Channel)
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
		return []string{tr.T(c.Lang, "no_messages")}, nil
	}

	// Busiest clock hour; ties resolve to the earliest hour.
	peakHour, peakCount := 0, 0
	for h, cnt := range hourCounts {
		if cnt > peakCount {
			peakHour, peakCount = h, cnt
		}
	}

	msg := tr.T(c.Lang, "summary", total, len(users), peakHour, peakCount)
	return []string{msg}, nil
}
