// Package alerts polls one or more alert sources (an FPV/UAV incident page,
// an RSS feed, etc.) and calls OnEvent for each newly-published item, so it
// can be relayed to a MeshCore channel. The bridge has no dependency on
// meshcore/dispatch; delivery is left to the caller via the OnEvent
// callback, same as tgbridge.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultPollIntervalSec = 60
	defaultMaxPerPoll      = 3
	fetchTimeout           = 10 * time.Second
)

// fetchFunc retrieves the current item list for one source, oldest-first.
type fetchFunc func(ctx context.Context, httpClient *http.Client, url string) ([]Event, error)

// source is one configured alert source paired with its runtime state.
type source struct {
	cfg   SourceConfig
	fetch fetchFunc
	seen  *SeenSet
}

// Bridge polls its configured sources, each on its own timer, and calls
// OnEvent for each new item.
type Bridge struct {
	httpClient *http.Client
	logger     *slog.Logger
	sources    []*source
	onEvent    func(sourceName, channel, postfix string, ev Event)
	done       chan struct{}
	wg         sync.WaitGroup
}

// New creates a Bridge, loading each source's seen-items store. Call Start()
// to begin polling.
func New(cfg Config, logger *slog.Logger, httpClient *http.Client) (*Bridge, error) {
	b := &Bridge{
		httpClient: httpClient,
		logger:     logger.With("module", "alerts"),
		done:       make(chan struct{}),
	}

	for _, sc := range cfg.Sources {
		if !sc.Enabled {
			continue
		}
		if sc.PollIntervalSec <= 0 {
			sc.PollIntervalSec = defaultPollIntervalSec
		}
		if sc.MaxPerPoll <= 0 {
			sc.MaxPerPoll = defaultMaxPerPoll
		}

		var fetch fetchFunc
		switch sc.Type {
		case "html_cards":
			fetch = fetchHTMLCards
		case "rss":
			fetch = fetchRSS
		default:
			return nil, fmt.Errorf("alerts: source %q: unknown type %q", sc.Name, sc.Type)
		}

		seen, err := NewSeenSet(filepath.Join("log", "alerts", sc.Name+".txt"))
		if err != nil {
			return nil, fmt.Errorf("alerts: source %q: %w", sc.Name, err)
		}

		b.sources = append(b.sources, &source{cfg: sc, fetch: fetch, seen: seen})
	}

	return b, nil
}

// OnEvent registers the callback invoked for each new item (oldest first per
// poll), identifying which source and channel it came from. postfix is the
// source's configured Postfix (may be empty) for the caller to append to the
// announced message text.
func (b *Bridge) OnEvent(fn func(sourceName, channel, postfix string, ev Event)) {
	b.onEvent = fn
}

// Start polls every configured source immediately, then on its own ticker.
// Blocks until Stop is called.
func (b *Bridge) Start() {
	for _, s := range b.sources {
		b.wg.Add(1)
		go b.runSource(s)
	}
	b.wg.Wait()
}

// Stop signals all poll loops to exit and waits for them to finish.
func (b *Bridge) Stop() {
	close(b.done)
	b.wg.Wait()
}

func (b *Bridge) runSource(s *source) {
	defer b.wg.Done()

	ticker := time.NewTicker(time.Duration(s.cfg.PollIntervalSec) * time.Second)
	defer ticker.Stop()

	b.poll(s)
	for {
		select {
		case <-b.done:
			return
		case <-ticker.C:
			b.poll(s)
		}
	}
}

// poll fetches the current item list for one source, records any new ids,
// and announces them (oldest first, capped at cfg.MaxPerPoll). On the very
// first poll of a fresh install it seeds the seen-set silently instead, so
// the channel isn't flooded with the source's entire history.
func (b *Bridge) poll(s *source) {
	log := b.logger.With("source", s.cfg.Name)

	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	events, err := s.fetch(ctx, b.httpClient, s.cfg.URL)
	if err != nil {
		log.Warn("fetch failed", "error", err)
		return
	}

	seeding := s.seen.Empty()

	var fresh []Event
	for _, ev := range events {
		if s.cfg.TitlePrefix != "" && !strings.HasPrefix(ev.Title, s.cfg.TitlePrefix) {
			continue
		}
		alreadyKnown, err := s.seen.CheckAndAdd(ev.ID)
		if err != nil {
			log.Error("recording seen id failed", "error", err, "id", ev.ID)
			continue
		}
		if !alreadyKnown {
			fresh = append(fresh, ev)
		}
	}

	if seeding {
		log.Info("seeded initial items, nothing announced", "count", len(fresh))
		return
	}
	if len(fresh) == 0 {
		return
	}

	if len(fresh) > s.cfg.MaxPerPoll {
		skipped := len(fresh) - s.cfg.MaxPerPoll
		fresh = fresh[len(fresh)-s.cfg.MaxPerPoll:]
		log.Warn("more new items than max_per_poll, skipping oldest", "skipped", skipped, "max_per_poll", s.cfg.MaxPerPoll)
	}

	for _, ev := range fresh {
		if b.onEvent != nil {
			b.onEvent(s.cfg.Name, s.cfg.Channel, s.cfg.Postfix, ev)
		}
	}
}
