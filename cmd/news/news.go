package main

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

	"meshbot/internal/config"
)

func main() {
	err := config.Load("config.yaml")
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))

	rssCfg := config.Get().RSS
	if !rssCfg.Enabled {
		slog.Info("RSS not enabled in config, exiting")
		return
	}

	if err := os.MkdirAll("log/news", 0o755); err != nil {
		slog.Error("Failed to create log/news dir", "error", err)
		os.Exit(1)
	}

	seenDir := rssCfg.SeenDir
	if seenDir == "" {
		seenDir = "log/news"
	}

	var wg sync.WaitGroup
	for _, f := range rssCfg.Feeds {
		if f.Name == "" {
			slog.Warn("RSS feed has no name, skipping", "url", f.URL)
			continue
		}
		dur, err := time.ParseDuration(f.Interval)
		if err != nil {
			slog.Error("RSS: invalid interval, skipping feed", "url", f.URL, "interval", f.Interval, "error", err)
			continue
		}
		wg.Add(1)
		go func(feedCfg config.RSSFeedConfig, interval time.Duration) {
			defer wg.Done()
			runFeedLoop(feedCfg, interval, seenDir)
		}(f, dur)
	}

	slog.Info("News poller started")
	wg.Wait()
}

// feedState holds per-feed polling state.
type feedState struct {
	name       string
	url        string
	newsFile   string
	seenFile   string
	seen       map[string]struct{}
	mu         sync.Mutex
	firstPoll  bool
	seenExists bool
}

func runFeedLoop(f config.RSSFeedConfig, interval time.Duration, seenDir string) {
	state := &feedState{
		name:      f.Name,
		url:       f.URL,
		newsFile:  fmt.Sprintf("log/news/%s.txt", f.Name),
		seenFile:  fmt.Sprintf("%s/%s.seen", seenDir, f.Name),
		seen:      make(map[string]struct{}),
		firstPoll: true,
	}
	if err := state.loadSeen(); err != nil {
		slog.Warn("News: failed to load seen file", "feed", f.Name, "error", err)
	}

	state.poll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		state.poll()
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

func (s *feedState) poll() {
	items, err := fetchFeed(s.url)
	if err != nil {
		slog.Warn("News: fetch error", "feed", s.name, "error", err)
		return
	}

	// On first ever run (no seen file), skip posting all current items
	// so we don't flood the file on startup.
	skipSaving := s.firstPoll && !s.seenExists

	// Process oldest-first so the file stays in chronological order.
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		id := strings.TrimSpace(item.GUID)
		if id == "" {
			id = strings.TrimSpace(item.Link)
		}
		if id == "" {
			continue
		}

		s.mu.Lock()
		_, already := s.seen[id]
		s.mu.Unlock()
		if already {
			continue
		}

		if skipSaving {
			s.markSeen(id)
			continue
		}

		title := strings.TrimSpace(item.Title)
		if title == "" {
			s.markSeen(id)
			continue
		}

		if err := s.appendNews(title); err != nil {
			slog.Warn("News: failed to save item", "feed", s.name, "error", err)
			continue
		}
		slog.Info("News: saved", "feed", s.name, "title", title)
		s.markSeen(id)
	}

	if s.firstPoll {
		s.firstPoll = false
		if skipSaving {
			slog.Info("News: initialized feed (existing items skipped)", "feed", s.name, "count", len(items))
		}
	}
}

func (s *feedState) markSeen(id string) {
	s.mu.Lock()
	s.seen[id] = struct{}{}
	s.mu.Unlock()
	s.appendSeen(id)
}

func (s *feedState) loadSeen() error {
	f, err := os.Open(s.seenFile)
	if os.IsNotExist(err) {
		s.seenExists = false
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	s.seenExists = true
	scanner := bufio.NewScanner(f)
	s.mu.Lock()
	defer s.mu.Unlock()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			s.seen[line] = struct{}{}
		}
	}
	return scanner.Err()
}

func (s *feedState) appendSeen(id string) {
	f, err := os.OpenFile(s.seenFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("News: failed to persist seen id", "feed", s.name, "error", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, id)
}

func (s *feedState) appendNews(line string) error {
	f, err := os.OpenFile(s.newsFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
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
