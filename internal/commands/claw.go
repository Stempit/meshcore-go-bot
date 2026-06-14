package commands

import (
	"context"
	"fmt"
	"log/slog"
	"meshbot/internal/config"
	"meshbot/internal/picoclaw"
	"strings"
	"time"
)

func ProcessClawCommand(text, senderKey, channelName string) ([]string, error) {
	prompt := strings.TrimSpace(strings.TrimPrefix(text, ".claw"))
	if prompt == "" {
		return []string{"usage: .claw <вопрос>"}, nil
	}

	cfg := config.Get()
	if !cfg.Claw.Enabled {
		return []string{"picoclaw не настроен"}, nil
	}

	timeoutSec := cfg.Claw.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 180
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	sessionID := clawSessionID(senderKey, channelName)
	parts, err := picoclaw.Ask(ctx, cfg.Claw.URL, cfg.Claw.Token, sessionID, prompt)
	if err != nil {
		slog.Warn("picoclaw request failed", "error", err)
		return []string{"ошибка соединения с picoclaw"}, nil
	}
	slog.Info("picoclaw response", "parts", len(parts))

	// Each picoclaw part is a separate logical message; mesh-fit shrinking
	// (the "claw" split override) is applied at the send site.
	return parts, nil
}

// clawSessionID derives a stable, URL-safe session identifier.
// Channel messages share one session per channel; DMs get one session per sender.
// senderKey is raw bytes (from contact.PublicKey); we hex-encode to avoid
// JSON encoding corrupting non-UTF-8 byte sequences.
func clawSessionID(senderKey, channelName string) string {
	if channelName != "" {
		return fmt.Sprintf("mesh-ch-%x", []byte(channelName))
	}
	if len(senderKey) >= 8 {
		return fmt.Sprintf("mesh-dm-%x", []byte(senderKey)[:8])
	}
	return "mesh-dm-default"
}
