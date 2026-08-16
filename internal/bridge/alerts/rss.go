package alerts

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// rssPubDateLayout matches this feed's pubDate format, e.g.
// "Mon, 27 Jul 26 13:10:00 +0300" — a 2-digit year, so time.RFC1123Z (which
// expects 4 digits) doesn't parse it.
const rssPubDateLayout = "Mon, 02 Jan 06 15:04:05 -0700"

type rssDoc struct {
	Channel struct {
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			PubDate string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

// fetchRSS retrieves and parses a standard RSS 2.0 feed, returning events
// oldest-first (the feed itself lists newest-first).
func fetchRSS(ctx context.Context, httpClient *http.Client, feedURL string) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; meshbot-alerts/1.0)")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rss feed status %d", resp.StatusCode)
	}

	var doc rssDoc
	if err := xml.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse rss: %w", err)
	}

	events := make([]Event, 0, len(doc.Channel.Items))
	for _, item := range doc.Channel.Items {
		title := strings.TrimSpace(item.Title)
		link := strings.TrimSpace(item.Link)
		if title == "" || link == "" {
			continue
		}

		var date, timeStr string
		if t, err := time.Parse(rssPubDateLayout, strings.TrimSpace(item.PubDate)); err == nil {
			date = t.Format("02.01.2006")
			timeStr = t.Format("15:04")
		}

		events = append(events, Event{ID: link, Date: date, Time: timeStr, Title: title})
	}

	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}
