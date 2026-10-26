// Package claw routes .claw and @[BotName] mentions to the picoclaw AI agent.
// It is an interceptor: the @[BotName] mention is matched at a Priority above
// bbs so a mention beats an active BBS session.
package claw

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/stempit/meshcore-go-bot/internal/bridge/picoclaw"
	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

// clawPriority sits above bbs (10) so a @[BotName] mention beats BBS mode.
const clawPriority = 20

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "claw",
		Triggers:  []string{".claw"},
		Match:     registry.Prefix,
		HelpShort: "ask picoclaw",
		HelpLong:  ".claw <question>\nask the picoclaw AI agent",
		Group:     "AI",
		Priority:  clawPriority,
	}
}

// LocalizedHelp returns the per-language long help for ".claw help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

// Matches handles both the @[BotName] mention (when configured) and the .claw
// trigger, reproducing the command's two original entry points.
func (plugin) Matches(c registry.Ctx) bool {
	if conf.Enabled && conf.BotName != "" {
		mp := strings.ToLower("@[" + conf.BotName + "]")
		if strings.HasPrefix(c.Lower, mp) {
			return true
		}
	}
	return c.Lower == ".claw" || strings.HasPrefix(c.Lower, ".claw ")
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	// @[BotName] mention: rewrite to a .claw prompt.
	if conf.Enabled && conf.BotName != "" {
		mentionPrefix := "@[" + conf.BotName + "]"
		if strings.HasPrefix(c.Lower, strings.ToLower(mentionPrefix)) {
			prompt := strings.TrimSpace(c.Trimmed[len(mentionPrefix):])
			return processClaw(c.Lang, ".claw "+prompt, c.SenderKey, c.Channel)
		}
	}
	return processClaw(c.Lang, c.Text, c.SenderKey, c.Channel)
}

func processClaw(lang, text, senderKey, channelName string) ([]string, error) {
	prompt := strings.TrimSpace(strings.TrimPrefix(text, ".claw"))
	if prompt == "" {
		return []string{tr.T(lang, "usage")}, nil
	}

	if !conf.Enabled {
		return []string{tr.T(lang, "not_configured")}, nil
	}

	timeoutSec := conf.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 180
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	sessionID := clawSessionID(senderKey, channelName)
	parts, err := picoclaw.Ask(ctx, conf.URL, conf.Token, sessionID, prompt)
	if err != nil {
		log().Warn("picoclaw request failed", "error", err)
		return []string{tr.T(lang, "connection_error")}, nil
	}
	log().Info("picoclaw response", "parts", len(parts))

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
