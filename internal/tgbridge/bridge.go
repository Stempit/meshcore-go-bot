// Package tgbridge polls Telegram sources and forwards messages to MeshCore.
package tgbridge

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

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
	Shrink          bool   `yaml:"shrink"`            // shrink long messages via LLM instead of splitting
}

// Bridge polls configured Telegram sources and calls OnMessage for each relevant message.
type Bridge struct {
	cfg    TelegramConfig
	bot    *tgbotapi.BotAPI
	onMsg  func(meshChannelName, senderName, text string, shrink bool)
	logger *slog.Logger
	done   chan struct{}
}

// New creates a Bridge and connects the Telegram bot. Call Start() to begin polling.
// httpClient is used for all Telegram API requests; pass a proxy-aware client if needed.
func New(cfg TelegramConfig, logger *slog.Logger, httpClient *http.Client) (*Bridge, error) {
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
	}, nil
}

// OnMessage registers the callback invoked for each matched message.
// meshChannelName is the MeshCore channel name from the source config.
func (b *Bridge) OnMessage(fn func(meshChannelName, senderName, text string, shrink bool)) {
	b.onMsg = fn
}

// Start begins long-polling for updates. Blocks until Stop() is called.
func (b *Bridge) Start() {
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

// Stop signals the polling loop to exit.
func (b *Bridge) Stop() {
	close(b.done)
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

// Forward sends a MeshCore channel message to every Telegram target configured for that channel.
// Call this from the incoming-message handler in bot.go for ChannelMessage events.
func (b *Bridge) Forward(meshChannelName, senderName, text string) {
	formatted := fmt.Sprintf("[%s] %s", senderName, text)
	for _, t := range b.cfg.Targets {
		if t.MeshChannelName != meshChannelName {
			continue
		}
		mc := tgbotapi.NewMessage(t.ChatID, formatted)
		if t.ThreadID != 0 {
			mc.MessageThreadID = t.ThreadID
		}
		if _, err := b.bot.Send(mc); err != nil {
			b.logger.Error("Telegram bridge: forward to Telegram failed",
				"chat_id", t.ChatID, "mesh_channel", meshChannelName, "error", err)
		} else {
			b.logger.Info("Telegram bridge: forwarded to Telegram",
				"chat_id", t.ChatID, "mesh_channel", meshChannelName, "sender", senderName)
		}
	}
}

func (b *Bridge) emit(src Source, senderName, text string) {
	if b.onMsg != nil {
		b.onMsg(src.MeshChannelName, senderName, text, src.Shrink)
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
