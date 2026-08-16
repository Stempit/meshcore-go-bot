// Package top answers .top with the most active channel authors.
package top

import (
	"embed"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"meshbot/internal/config"
	"meshbot/internal/support/helpers"
	"meshbot/internal/support/historylog"
	"meshbot/internal/support/i18n"
	"meshbot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

const (
	defaultTopScanCount = 100 // recent messages scanned when scan_count is unset
	topAuthorsShown     = 3   // how many ranked authors to return
	topNameMaxRunes     = 12  // author names are clipped to fit the 140-byte mesh limit
	topBarMax           = 5   // blocks in the longest bar (the leader)
	topBarChar          = "▇" // bar block; the leader gets topBarMax of these
)

// topMedals decorates the first three places; lower places fall back to a number.
var topMedals = []string{"🥇", "🥈", "🥉"}

// cfg controls how many recent messages are scanned. Configured once at startup.
type cfg struct {
	ScanCount int `yaml:"scan_count"` // recent messages to scan, default 100
}

var conf cfg

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "top",
		Triggers:  []string{".top"},
		Match:     registry.Exact,
		HelpShort: "top authors",
		HelpLong:  ".top — top-5 channel authors by message count",
		Group:     "Info",
	}
}

// LocalizedHelp returns the per-language long help for ".top help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

func (plugin) ConfigKey() string { return "Top" }

func (plugin) Configure(s config.Sectioner) error {
	return s.Section("Top", &conf)
}

func (plugin) Enabled() bool { return true }

// Handle ranks the most active authors in the channel where ".top" was called.
// It scans the last N channel messages (N from scan_count, default 100),
// excludes the bot's own ("Me") messages, and returns the top authors by
// message count. Channels only; in DMs it reports unavailable.
func (plugin) Handle(c registry.Ctx) ([]string, error) {
	if c.IsDM() {
		return []string{tr.T(c.Lang, "channels_only")}, nil
	}
	currentChannel := c.Channel

	entries, err := historylog.ReadAllChannel(helpers.HistoryDir, currentChannel)
	if err != nil {
		return nil, fmt.Errorf(".top: %w", err)
	}

	scanCount := conf.ScanCount
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
		return []string{tr.T(c.Lang, "no_messages")}, nil
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

	leader := counts[ranked[0]]             // ranked is sorted desc, so the first is the max
	countWidth := len(strconv.Itoa(leader)) // right-align counts into a column

	var b strings.Builder
	b.WriteString(tr.T(c.Lang, "header", len(msgs)))
	for i, name := range ranked {
		var place string
		if i < len(topMedals) {
			place = topMedals[i]
		} else {
			place = fmt.Sprintf("%d", i+1)
		}
		cnt := counts[name]
		blocks := barLen(cnt, leader)
		// Lead with a fixed-width bar so bars and counts form columns; the
		// variable-length name trails where its length no longer matters.
		bar := strings.Repeat(topBarChar, blocks) + strings.Repeat(" ", topBarMax-blocks)
		fmt.Fprintf(&b, "\n%s %s %*d %s", place, bar, countWidth, cnt, helpers.ClipRunes(name, topNameMaxRunes))
	}
	return []string{b.String()}, nil
}

// barLen scales a count to a bar of 1..topBarMax blocks, relative to the
// leader's count. Any nonzero count gets at least one block so it stays visible.
func barLen(count, leader int) int {
	if leader <= 0 {
		return 0
	}
	n := int(math.Round(float64(count) / float64(leader) * topBarMax))
	if n < 1 {
		n = 1
	}
	return n
}
