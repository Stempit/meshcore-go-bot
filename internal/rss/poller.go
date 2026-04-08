package rss

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Feed describes a single RSS feed to poll.
type Feed struct {
	URL      string
	Channel  string
	Prefix   string // prepended to every message, e.g. "[habr] "
	Interval time.Duration
}

// Poller fetches RSS feeds and posts new item titles to meshcore channels.
type Poller struct {
	feeds      []Feed
	longTitle  string // "skip" or "shrink"
	sendFunc   func(channelName, text string) error
	shrinkFunc func(text string) (string, error)
	seenFile   string

	seen           map[string]struct{}
	seenMu         sync.Mutex
	sendMu         sync.Mutex  // serializes all sendFunc calls across feed goroutines
	seenFileExists bool
	initialized    map[string]bool // feed URL -> first poll done
}

// New creates a Poller.
//
//   - longTitle: "skip" or "shrink" – what to do when a title exceeds 140 bytes.
//   - seenFile:  path to a file that persists seen GUIDs across restarts.
//   - sendFunc:  callback that posts a message to a named meshcore channel.
//   - shrinkFunc: callback that compresses a title via LLM (may be nil when longTitle=="skip").
func New(
	feeds []Feed,
	longTitle, seenFile string,
	sendFunc func(string, string) error,
	shrinkFunc func(string) (string, error),
) *Poller {
	return &Poller{
		feeds:       feeds,
		longTitle:   longTitle,
		sendFunc:    sendFunc,
		shrinkFunc:  shrinkFunc,
		seenFile:    seenFile,
		seen:        make(map[string]struct{}),
		initialized: make(map[string]bool),
	}
}

// Start loads the seen-file and launches one goroutine per feed.
func (p *Poller) Start() {
	if err := p.loadSeen(); err != nil {
		slog.Warn("RSS: failed to load seen file", "error", err)
	}
	for _, f := range p.feeds {
		go p.pollLoop(f)
	}
}

func (p *Poller) pollLoop(f Feed) {
	p.poll(f)
	ticker := time.NewTicker(f.Interval)
	defer ticker.Stop()
	for range ticker.C {
		p.poll(f)
	}
}

// rss XML models

type rssDoc struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
	GUID  string `xml:"guid"`
}

func (p *Poller) poll(f Feed) {
	items, err := fetchFeed(f.URL)
	if err != nil {
		slog.Warn("RSS: fetch error", "url", f.URL, "error", err)
		return
	}

	firstPoll := !p.initialized[f.URL]
	// On first ever run (no seen file), skip posting all current items
	// so we don't flood the channel on startup.
	skipPosting := firstPoll && !p.seenFileExists

	// Process oldest-first so channel gets items in chronological order.
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		id := strings.TrimSpace(item.GUID)
		if id == "" {
			id = strings.TrimSpace(item.Link)
		}
		if id == "" {
			continue
		}

		p.seenMu.Lock()
		_, already := p.seen[id]
		p.seenMu.Unlock()
		if already {
			continue
		}

		if skipPosting {
			p.markSeen(id)
			continue
		}

		title := strings.TrimSpace(item.Title)
		if title == "" {
			p.markSeen(id)
			continue
		}

		text, skip := p.prepareTitle(f.Prefix, title)
		if skip {
			slog.Info("RSS: skipping long title", "url", f.URL, "title", title)
			p.markSeen(id)
			continue
		}

		// Serialize all sends: sentConfirmCh in the BLE client is a single
		// shared slot — concurrent senders steal each other's confirmation.
		p.sendMu.Lock()
		err := p.sendFunc(f.Channel, text)
		p.sendMu.Unlock()

		if err != nil {
			slog.Warn("RSS: send error", "channel", f.Channel, "error", err)
			// Don't mark as seen – retry on next poll.
			continue
		}
		slog.Info("RSS: posted", "channel", f.Channel, "text", text)
		p.markSeen(id)

		// Brief pause between posts so the radio isn't flooded.
		time.Sleep(3 * time.Second)
	}

	if firstPoll {
		p.initialized[f.URL] = true
		if skipPosting {
			slog.Info("RSS: initialized feed (existing items skipped)", "url", f.URL, "count", len(items))
		}
	}
}

func (p *Poller) prepareTitle(prefix, title string) (text string, skip bool) {
	const maxLen = 140
	full := prefix + title
	if len(full) <= maxLen {
		return full, false
	}
	switch p.longTitle {
	case "shrink":
		if p.shrinkFunc == nil {
			return "", true
		}
		// Ask LLM to shrink the title to fit within the budget after the prefix.
		budget := maxLen - len(prefix)
		if budget <= 0 {
			return "", true
		}
		shrunk, err := p.shrinkFunc(title)
		if err != nil {
			slog.Warn("RSS: shrink error", "error", err)
			return "", true
		}
		if len(shrunk) > budget {
			shrunk = shrunk[:budget]
		}
		return prefix + shrunk, false
	default: // "skip" or anything else
		return "", true
	}
}

func (p *Poller) markSeen(id string) {
	p.seenMu.Lock()
	p.seen[id] = struct{}{}
	p.seenMu.Unlock()
	p.appendSeen(id)
}

func (p *Poller) loadSeen() error {
	f, err := os.Open(p.seenFile)
	if os.IsNotExist(err) {
		p.seenFileExists = false
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	p.seenFileExists = true
	scanner := bufio.NewScanner(f)
	p.seenMu.Lock()
	defer p.seenMu.Unlock()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			p.seen[line] = struct{}{}
		}
	}
	return scanner.Err()
}

func (p *Poller) appendSeen(id string) {
	f, err := os.OpenFile(p.seenFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("RSS: failed to persist seen id", "error", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, id)
}

func fetchFeed(url string) ([]rssItem, error) {
	resp, err := http.Get(url) //nolint:gosec // URL comes from trusted config
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var doc rssDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("xml parse: %w", err)
	}
	return doc.Items, nil
}
