// Package news answers .news by reading per-feed files produced by the separate
// newswire poller (https://github.com/Stempit/newswire).
package news

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"meshbot/internal/commands/registry"
)

const newsDir = "log/news"
const defaultNewsCount = 5

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "news",
		Triggers:  []string{".news"},
		Match:     registry.Prefix,
		HelpShort: "rss feeds",
		HelpLong:  ".news — list feeds\n.news <feed> [n] [offset] — recent items",
		Group:     "Info",
	}
}

// Handle implements the .news command variants:
//
//	.news                        – list available feeds
//	.news <feed>                 – last 5 items
//	.news <feed> <n>             – last n items
//	.news <feed> <n> <offset>    – last n items before the last <offset> items
//
// Each returned string is sent as a separate message.
func (plugin) Handle(c registry.Ctx) ([]string, error) {
	parts := strings.Fields(c.Text)
	// parts[0] is ".news"

	if len(parts) == 1 {
		return listFeeds()
	}

	feedName := parts[1]
	count := defaultNewsCount
	offset := 0

	if len(parts) >= 3 {
		n, err := strconv.Atoi(parts[2])
		if err != nil || n <= 0 {
			return []string{"usage: .news <feed> [count] [offset]"}, nil
		}
		count = n
	}
	if len(parts) >= 4 {
		o, err := strconv.Atoi(parts[3])
		if err != nil || o < 0 {
			return []string{"usage: .news <feed> [count] [offset]"}, nil
		}
		offset = o
	}

	return readNews(feedName, count, offset)
}

func listFeeds() ([]string, error) {
	entries, err := os.ReadDir(newsDir)
	if os.IsNotExist(err) {
		return []string{"no feeds yet"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read news dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".txt" {
			names = append(names, strings.TrimSuffix(e.Name(), ".txt"))
		}
	}
	if len(names) == 0 {
		return []string{"no feeds yet"}, nil
	}
	return []string{"feeds: " + strings.Join(names, ", ")}, nil
}

func readNews(feedName string, count, offset int) ([]string, error) {
	path := filepath.Join(newsDir, feedName+".txt")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []string{fmt.Sprintf("feed %q not found", feedName)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open news file: %w", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read news file: %w", err)
	}

	total := len(lines)
	if total == 0 {
		return []string{fmt.Sprintf("no news in %s", feedName)}, nil
	}

	// lines are ordered oldest→newest; we want items from the end.
	// skip the last <offset> items, then take <count> before that.
	end := total - offset
	if end <= 0 {
		return []string{fmt.Sprintf("offset %d exceeds available %d items", offset, total)}, nil
	}
	start := end - count
	if start < 0 {
		start = 0
	}

	selected := lines[start:end]

	// Return newest-first so the most recent message arrives last in chat.
	// Each line is a separate logical message; mesh-fit shrinking (incl. the
	// "news" skip override for long titles) is applied at the send site.
	result := make([]string, 0, len(selected))
	for i := len(selected) - 1; i >= 0; i-- {
		result = append(result, selected[i])
	}

	if len(result) == 0 {
		return []string{fmt.Sprintf("no news in %s", feedName)}, nil
	}
	return result, nil
}
