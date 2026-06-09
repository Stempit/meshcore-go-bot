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

	var replies []string
	for _, part := range parts {
		if cfg.Claw.Split {
			replies = append(replies, splitForMesh(part, meshMaxBytes)...)
		} else {
			shrunk, err := ShrinkText(part)
			if err != nil {
				slog.Warn("picoclaw shrink failed", "error", err)
				replies = append(replies, part)
				continue
			}
			replies = append(replies, shrunk)
		}
	}
	return replies, nil
}

// meshMaxBytes is the target byte budget for a single mesh message.
const meshMaxBytes = 120

// splitForMesh splits text into chunks that each fit within maxBytes.
// When multiple chunks are produced each gets a "[i/N]" series suffix.
// Splits on space boundaries where possible; falls back to rune boundaries.
func splitForMesh(text string, maxBytes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= maxBytes {
		return []string{text}
	}

	// Reserve bytes for the largest marker we might need: " [99/99]" = 8 bytes.
	const markerReserve = 8
	chunkMax := maxBytes - markerReserve

	var chunks []string
	s := text
	for len(s) > 0 {
		if len(s) <= chunkMax {
			chunks = append(chunks, strings.TrimSpace(s))
			break
		}
		cut := chunkMax
		// Walk back past any UTF-8 continuation bytes so we don't split a rune.
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		// Prefer splitting at a space in the back half of the chunk.
		if idx := strings.LastIndex(s[:cut], " "); idx > chunkMax/2 {
			cut = idx
		}
		chunks = append(chunks, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}

	if len(chunks) <= 1 {
		return []string{text}
	}

	total := len(chunks)
	result := make([]string, total)
	for i, c := range chunks {
		result[i] = fmt.Sprintf("%s [%d/%d]", c, i+1, total)
	}
	return result
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
