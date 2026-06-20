// Package tgbridge polls Telegram sources and forwards messages to MeshCore.
package tgbridge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// Outbound delivery tuning. Forwarding to Telegram runs through a buffered queue
// drained by a single worker, so a slow or unreachable Telegram API can never
// block the mesh reply path. A single worker preserves message order; under a
// sustained outage the queue fills and newer messages are dropped (logged)
// rather than queued unbounded. Each send attempt is bounded by its own context
// timeout so it can't inherit the long-poll client's 60s timeout.
const (
	outboxCapacity = 256              // buffered jobs before Forward drops
	sendAttempts   = 3                // total send attempts per message
	sendTimeout    = 15 * time.Second // per-attempt timeout for sends
)

// sendBackoff is the wait before retry N (index = attempt-1). Its length must be
// at least sendAttempts-1.
var sendBackoff = []time.Duration{2 * time.Second, 5 * time.Second}

// tgJob is a single queued Telegram delivery.
type tgJob struct {
	chatID      int64
	threadID    int
	text        string
	meshChannel string // for logging
}

// TelegramConfig holds bot credentials and the list of sources to monitor.
type TelegramConfig struct {
	Enabled  bool     `yaml:"enabled"`
	BotToken string   `yaml:"bot_token"`
	Sources  []Source `yaml:"sources"`
	Targets  []Target `yaml:"targets"`
}

// Target describes a MeshCore channel whose messages are forwarded to Telegram.
// type is implicit: if thread_id != 0 the message is sent to a topic inside a group;
// otherwise it is sent to a plain channel or group (chat_id only).
type Target struct {
	MeshChannelName string `yaml:"mesh_channel_name"` // meshcore channel to listen on (e.g. "#tg")
	ChatID          int64  `yaml:"chat_id"`           // Telegram chat/channel ID to post to
	ThreadID        int    `yaml:"thread_id"`         // topic/thread ID (0 = no thread)
}

// Source describes a single Telegram source to monitor.
// type: "channel"      — a Telegram channel (chat_id required)
// type: "group_thread" — a topic/thread inside a public group (chat_id + thread_id required)
type Source struct {
	Type            string `yaml:"type"`
	ChatID          int64  `yaml:"chat_id"`           // channel or group ID (e.g. -1001234567890)
	ThreadID        int    `yaml:"thread_id"`         // topic/thread ID inside the group (group_thread only)
	MeshChannelName string `yaml:"mesh_channel_name"` // MeshCore channel to forward into (e.g. "#tg")
}

// Bridge polls configured Telegram sources and calls OnMessage for each relevant message.
type Bridge struct {
	cfg    TelegramConfig
	bot    *tgbotapi.BotAPI
	onMsg  func(meshChannelName, senderName, text string)
	logger *slog.Logger
	done   chan struct{}
	outbox chan tgJob
	wg     sync.WaitGroup
}

// New creates a Bridge and connects the Telegram bot. Call Start() to begin polling.
// httpClient is used for all Telegram API requests; pass a proxy-aware client if needed.
func New(cfg TelegramConfig, logger *slog.Logger, httpClient *http.Client) (*Bridge, error) {
	logger = logger.With("module", "tgbridge")
	// Long-poll timeout is 30s; the HTTP client must exceed it or it races.
	pollClient := *httpClient
	pollClient.Timeout = 60 * time.Second
	bot, err := tgbotapi.NewBotAPIWithClient(cfg.BotToken, tgbotapi.APIEndpoint, &pollClient)
	if err != nil {
		return nil, fmt.Errorf("telegram bot init: %w", err)
	}

	// Remove any active webhook — getUpdates returns nothing while a webhook is set.
	if _, err := bot.Request(tgbotapi.DeleteWebhookConfig{DropPendingUpdates: false}); err != nil {
		logger.Warn("Telegram bridge: failed to delete webhook", "error", err)
	} else {
		logger.Info("Telegram bridge: webhook cleared")
	}

	// Verify webhook is gone and log current bot info.
	if info, err := bot.GetWebhookInfo(); err == nil {
		logger.Info("Telegram bridge webhook info", "url", info.URL, "pending", info.PendingUpdateCount)
	}

	logger.Info("Telegram bridge connected", "bot", bot.Self.UserName, "sources", len(cfg.Sources))
	return &Bridge{
		cfg:    cfg,
		bot:    bot,
		logger: logger,
		done:   make(chan struct{}),
		outbox: make(chan tgJob, outboxCapacity),
	}, nil
}

// OnMessage registers the callback invoked for each matched message.
// meshChannelName is the MeshCore channel name from the source config.
func (b *Bridge) OnMessage(fn func(meshChannelName, senderName, text string)) {
	b.onMsg = fn
}

// Start begins long-polling for updates. Blocks until Stop() is called.
func (b *Bridge) Start() {
	// One outbound worker drains the queue, preserving Telegram message order.
	b.wg.Add(1)
	go b.sendWorker()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	u.AllowedUpdates = []string{"message", "channel_post"}

	updates := b.bot.GetUpdatesChan(u)
	b.logger.Info("Telegram bridge polling started")

	for {
		select {
		case <-b.done:
			b.bot.StopReceivingUpdates()
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			b.logUpdate(update)
			b.handleUpdate(update)
		}
	}
}

// Stop signals the polling loop and outbound worker to exit, then waits for the
// worker to finish any in-flight send.
func (b *Bridge) Stop() {
	close(b.done)
	b.wg.Wait()
}

func (b *Bridge) handleUpdate(update tgbotapi.Update) {
	for _, src := range b.cfg.Sources {
		switch src.Type {
		case "channel":
			b.handleChannel(update, src)
		case "group_thread":
			b.handleGroupThread(update, src)
		}
	}
}

// handleChannel forwards posts from a Telegram channel.
func (b *Bridge) handleChannel(update tgbotapi.Update, src Source) {
	post := update.ChannelPost
	if post == nil || post.Chat.ID != src.ChatID {
		return
	}

	text := messageText(post)
	if text == "" {
		return
	}

	senderName := post.Chat.Title
	if senderName == "" {
		senderName = "TG"
	}

	b.logger.Info("Telegram channel post", "chat", senderName, "mesh_channel", src.MeshChannelName, "text", text)
	b.emit(src, senderName, text)
}

// handleGroupThread forwards messages from a specific topic/thread in a group.
func (b *Bridge) handleGroupThread(update tgbotapi.Update, src Source) {
	msg := update.Message
	if msg == nil {
		return
	}
	if msg.Chat.ID != src.ChatID {
		return
	}
	if !msg.IsTopicMessage || msg.MessageThreadID != src.ThreadID {
		return
	}

	text := messageText(msg)
	if text == "" {
		return
	}

	senderName := senderDisplayName(msg)
	b.logger.Info("Telegram group thread message",
		"group", msg.Chat.Title,
		"thread", src.ThreadID,
		"from", senderName,
		"mesh_channel", src.MeshChannelName,
		"text", text,
	)
	b.emit(src, senderName, text)
}

func (b *Bridge) logUpdate(update tgbotapi.Update) {
	switch {
	case update.ChannelPost != nil:
		b.logger.Debug("Telegram update: channel_post",
			"chat_id", update.ChannelPost.Chat.ID,
			"chat_title", update.ChannelPost.Chat.Title,
			"text", update.ChannelPost.Text,
		)
	case update.Message != nil:
		b.logger.Debug("Telegram update: message",
			"chat_id", update.Message.Chat.ID,
			"chat_title", update.Message.Chat.Title,
			"from", senderDisplayName(update.Message),
			"is_topic", update.Message.IsTopicMessage,
			"thread_id", update.Message.MessageThreadID,
			"text", update.Message.Text,
		)
	default:
		b.logger.Debug("Telegram update: other", "update_id", update.UpdateID)
	}
}

// HasTarget reports whether any Telegram target is configured for the given mesh channel name.
func (b *Bridge) HasTarget(meshChannelName string) bool {
	for _, t := range b.cfg.Targets {
		if t.MeshChannelName == meshChannelName {
			return true
		}
	}
	return false
}

// Forward queues a MeshCore channel message for every Telegram target configured
// for that channel. It never blocks on the network: jobs go onto a buffered queue
// drained by sendWorker, so a slow Telegram API can't hold up the mesh reply path.
// If the queue is full (sustained Telegram outage) the message is dropped and logged.
// Call this from the incoming-message handler in bot.go for ChannelMessage events.
func (b *Bridge) Forward(meshChannelName, senderName, text string) {
	formatted := fmt.Sprintf("[%s] %s", senderName, text)
	for _, t := range b.cfg.Targets {
		if t.MeshChannelName != meshChannelName {
			continue
		}
		job := tgJob{chatID: t.ChatID, threadID: t.ThreadID, text: formatted, meshChannel: meshChannelName}
		select {
		case b.outbox <- job:
		default:
			b.logger.Warn("Telegram bridge: outbox full, dropping message",
				"chat_id", t.ChatID, "mesh_channel", meshChannelName)
		}
	}
}

// sendWorker drains the outbox, delivering one job at a time so Telegram messages
// keep their order. It exits when Stop closes done.
func (b *Bridge) sendWorker() {
	defer b.wg.Done()
	for {
		select {
		case <-b.done:
			return
		case job := <-b.outbox:
			b.deliver(job)
		}
	}
}

// deliver sends one job, retrying up to sendAttempts times with backoff. Each
// attempt is bounded by sendTimeout. Backoff waits abort early if Stop is called.
func (b *Bridge) deliver(job tgJob) {
	mc := tgbotapi.NewMessage(job.chatID, job.text)
	if job.threadID != 0 {
		mc.MessageThreadID = job.threadID
	}

	for attempt := 1; attempt <= sendAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		_, err := b.bot.RequestWithContext(ctx, mc)
		cancel()
		if err == nil {
			b.logger.Info("Telegram bridge: forwarded to Telegram",
				"chat_id", job.chatID, "mesh_channel", job.meshChannel, "attempt", attempt)
			return
		}
		b.logger.Warn("Telegram bridge: forward attempt failed",
			"chat_id", job.chatID, "mesh_channel", job.meshChannel,
			"attempt", attempt, "max_attempts", sendAttempts, "error", err)

		if attempt < sendAttempts {
			select {
			case <-b.done:
				return
			case <-time.After(sendBackoff[attempt-1]):
			}
		}
	}

	b.logger.Error("Telegram bridge: forward to Telegram failed, giving up",
		"chat_id", job.chatID, "mesh_channel", job.meshChannel, "attempts", sendAttempts)
}

func (b *Bridge) emit(src Source, senderName, text string) {
	if b.onMsg != nil {
		b.onMsg(src.MeshChannelName, senderName, text)
	}
}

// messageText extracts displayable text from a message (text or media caption).
func messageText(msg *tgbotapi.Message) string {
	if msg.Text != "" {
		return msg.Text
	}
	return strings.TrimSpace(msg.Caption)
}

// senderDisplayName returns a human-readable name for the message author.
func senderDisplayName(msg *tgbotapi.Message) string {
	if msg.From == nil {
		return "?"
	}
	name := msg.From.FirstName
	if msg.From.LastName != "" {
		name += " " + msg.From.LastName
	}
	if name == "" {
		name = msg.From.UserName
	}
	if name == "" {
		name = "?"
	}
	return name
}
