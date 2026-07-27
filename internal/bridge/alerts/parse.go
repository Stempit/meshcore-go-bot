package alerts

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// Event is one incident parsed from the alert page.
type Event struct {
	ID    string // absolute permalink URL, used as the dedup key
	Date  string // DD.MM.YYYY
	Time  string // HH:MM
	Title string
}

// articleClassPrefix identifies an incident card's <article> wrapper on the
// bplarussia.ru region pages. Verified against the live site's markup.
const articleClassPrefix = "bg-white rounded-xl shadow-sm border border-gray-200"

// fetchEvents retrieves and parses the alert page, returning events oldest-first
// (the page itself lists newest-first).
func fetchEvents(ctx context.Context, httpClient *http.Client, pageURL string) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
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
		return nil, fmt.Errorf("alerts page status %d", resp.StatusCode)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	var events []Event
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "article" && hasClassPrefix(n, articleClassPrefix) {
			if ev, ok := parseArticle(n); ok {
				events = append(events, ev)
			}
			return // cards don't nest
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}

// parseArticle extracts one incident from a single <article> card.
//
// Card layout (verified against the live page):
//
//	<span class="text-sm font-medium">12:56</span>   <!-- Time, after the clock icon -->
//	<span class="text-sm">27.07.2026</span>           <!-- Date, after the calendar icon -->
//	<h2 class="text-xl font-bold ...">
//	  <a href="https://.../permalink/">Title text</a>
//	</h2>
//
// A status badge and a <p> description also exist per card but are not
// captured: MeshCore messages are limited to ~140 bytes, so only the short
// title (plus a date/time prefix) is ever announced.
func parseArticle(n *html.Node) (Event, bool) {
	var ev Event
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "span":
				switch classAttr(n) {
				case "text-sm font-medium":
					if ev.Time == "" {
						ev.Time = strings.TrimSpace(textContent(n))
					}
				case "text-sm":
					if ev.Date == "" {
						ev.Date = strings.TrimSpace(textContent(n))
					}
				}
			case "h2":
				if ev.Title == "" && strings.HasPrefix(classAttr(n), "text-xl font-bold") {
					if a := findChildElement(n, "a"); a != nil {
						ev.ID = attrOf(a, "href")
						ev.Title = strings.TrimSpace(textContent(a))
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)

	if ev.ID == "" || ev.Title == "" {
		return Event{}, false
	}
	return ev, true
}

func attrOf(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func classAttr(n *html.Node) string {
	return strings.TrimSpace(attrOf(n, "class"))
}

func hasClassPrefix(n *html.Node, prefix string) bool {
	return strings.HasPrefix(classAttr(n), prefix)
}

func findChildElement(n *html.Node, tag string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			return c
		}
		if found := findChildElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}
