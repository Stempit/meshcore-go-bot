// Package alerts polls an FPV/UAV alert page (e.g. bplarussia.ru) and calls
// OnEvent for each newly-published incident, so it can be relayed to a
// MeshCore channel. The bridge has no dependency on meshcore/dispatch;
// delivery is left to the caller via the OnEvent callback, same as tgbridge.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	defaultPollIntervalSec = 60
	defaultMaxPerPoll      = 3
	fetchTimeout           = 10 * time.Second

	// seenPath persists incident permalinks already processed, so a restart
	// doesn't re-announce (or re-seed) history.
	seenPath = "log/alerts/seen.txt"
)

// Bridge polls an alert page on a timer and calls OnEvent for each new incident.
type Bridge struct {
	cfg        Config
	httpClient *http.Client
	logger     *slog.Logger
	seen       *SeenSet
	onEvent    func(Event)
	done       chan struct{}
	wg         sync.WaitGroup
}

// New creates a Bridge, loading its seen-incidents store. Call Start() to
// begin polling.
func New(cfg Config, logger *slog.Logger, httpClient *http.Client) (*Bridge, error) {
	if cfg.PollIntervalSec <= 0 {
		cfg.PollIntervalSec = defaultPollIntervalSec
	}
	if cfg.MaxPerPoll <= 0 {
		cfg.MaxPerPoll = defaultMaxPerPoll
	}

	seen, err := NewSeenSet(seenPath)
	if err != nil {
		return nil, fmt.Errorf("alerts: %w", err)
	}

	return &Bridge{
		cfg:        cfg,
		httpClient: httpClient,
		logger:     logger.With("module", "alerts"),
		seen:       seen,
		done:       make(chan struct{}),
	}, nil
}

// OnEvent registers the callback invoked for each new incident (oldest first).
func (b *Bridge) OnEvent(fn func(Event)) {
	b.onEvent = fn
}

// Start polls the alert page immediately, then on a ticker. Blocks until Stop
// is called.
func (b *Bridge) Start() {
	b.wg.Add(1)
	defer b.wg.Done()

	ticker := time.NewTicker(time.Duration(b.cfg.PollIntervalSec) * time.Second)
	defer ticker.Stop()

	b.poll()
	for {
		select {
		case <-b.done:
			return
		case <-ticker.C:
			b.poll()
		}
	}
}

// Stop signals the poll loop to exit and waits for it to finish.
func (b *Bridge) Stop() {
	close(b.done)
	b.wg.Wait()
}

// poll fetches the current incident list, records any new permalinks, and
// announces them (oldest first, capped at cfg.MaxPerPoll). On the very first
// poll of a fresh install it seeds the seen-set silently instead, so the
// channel isn't flooded with the site's entire history.
func (b *Bridge) poll() {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	events, err := fetchEvents(ctx, b.httpClient, b.cfg.URL)
	if err != nil {
		b.logger.Warn("fetch failed", "error", err)
		return
	}

	seeding := b.seen.Empty()

	var fresh []Event
	for _, ev := range events {
		alreadyKnown, err := b.seen.CheckAndAdd(ev.ID)
		if err != nil {
			b.logger.Error("recording seen id failed", "error", err, "id", ev.ID)
			continue
		}
		if !alreadyKnown {
			fresh = append(fresh, ev)
		}
	}

	if seeding {
		b.logger.Info("seeded initial incidents, nothing announced", "count", len(fresh))
		return
	}
	if len(fresh) == 0 {
		return
	}

	if len(fresh) > b.cfg.MaxPerPoll {
		skipped := len(fresh) - b.cfg.MaxPerPoll
		fresh = fresh[len(fresh)-b.cfg.MaxPerPoll:]
		b.logger.Warn("more new incidents than max_per_poll, skipping oldest", "skipped", skipped, "max_per_poll", b.cfg.MaxPerPoll)
	}

	for _, ev := range fresh {
		if b.onEvent != nil {
			b.onEvent(ev)
		}
	}
}
