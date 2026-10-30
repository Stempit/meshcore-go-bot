// Package agent routes .agent and @[BotName] mentions to the user's AI agent
// (nanobot behind the OpenAI-compatible adapter, a legacy picoclaw gateway, or
// any other backend of internal/bridge/agentclient).
// It is an interceptor: the @[BotName] mention is matched at a Priority above
// bbs so a mention beats an active BBS session.
package agent

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
	"github.com/stempit/meshcore-go-bot/internal/support/netgate"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

// agentPriority sits above bbs (10) so a @[BotName] mention beats BBS mode.
const agentPriority = 20

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "agent",
		Triggers:  []string{".agent"},
		Match:     registry.Prefix,
		HelpShort: "ask your AI agent",
		HelpLong:  ".agent <question>\nask your AI agent",
		Group:     "AI",
		Priority:  agentPriority,
	}
}

// LocalizedHelp returns the per-language long help for ".agent help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

// Matches handles both the @[BotName] mention (when configured) and the .agent
// trigger, reproducing the command's two entry points.
func (plugin) Matches(c registry.Ctx) bool {
	if conf.Enabled && conf.BotName != "" {
		mp := strings.ToLower("@[" + conf.BotName + "]")
		if strings.HasPrefix(c.Lower, mp) {
			return true
		}
	}
	return c.Lower == ".agent" || strings.HasPrefix(c.Lower, ".agent ")
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	// @[BotName] mention: rewrite to an .agent prompt.
	if conf.Enabled && conf.BotName != "" {
		mentionPrefix := "@[" + conf.BotName + "]"
		if strings.HasPrefix(c.Lower, strings.ToLower(mentionPrefix)) {
			prompt := mentionPrompt(c.Trimmed, mentionPrefix)
			return processAgent(c.Lang, prompt, c.SenderKey, c.Channel)
		}
	}
	// agent is a Matcher interceptor, so Dispatch leaves Rest empty; cut the
	// trigger with the case-insensitive Arg helper instead.
	return processAgent(c.Lang, c.Arg(".agent"), c.SenderKey, c.Channel)
}

// mentionPrompt extracts the prompt after a mention that matched
// case-insensitively. The cut is by rune count: ToLower preserves rune count
// but not byte length (İ → i), so slicing Trimmed by len(lowercased prefix)
// could panic or mis-cut for names containing such runes.
func mentionPrompt(trimmed, mentionPrefix string) string {
	n := utf8.RuneCountInString(strings.ToLower(mentionPrefix))
	runes := []rune(trimmed)
	if n > len(runes) {
		return ""
	}
	return strings.TrimSpace(string(runes[n:]))
}

func processAgent(lang, prompt, senderKey, channelName string) ([]string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return []string{tr.T(lang, "usage")}, nil
	}

	if !conf.Enabled {
		return []string{tr.T(lang, "not_configured")}, nil
	}

	// Bound pool parking: 8 distinct senders × 180s timeouts would otherwise
	// pin the whole dispatcher worker pool. Reply busy when full.
	release, ok := netgate.TryAcquire()
	if !ok {
		return []string{tr.T(lang, "busy")}, nil
	}
	defer release()

	timeoutSec := conf.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 180
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	sessionID := agentSessionID(senderKey, channelName)
	parts, err := client.Ask(ctx, sessionID, prompt)
	if err != nil {
		log().Warn("agent request failed", "error", err)
		return []string{tr.T(lang, "connection_error")}, nil
	}
	log().Info("agent response", "parts", len(parts), "type", conf.Type)

	// Each agent part is a separate logical message; mesh-fit shrinking
	// (the "agent" split override) is applied at the send site.
	return parts, nil
}

// agentSessionID derives a stable, URL-safe session identifier.
// Channel messages share one session per channel; DMs get one session per sender.
// senderKey is raw bytes (from contact.PublicKey); we hex-encode to avoid
// JSON encoding corrupting non-UTF-8 byte sequences.
func agentSessionID(senderKey, channelName string) string {
	if channelName != "" {
		return fmt.Sprintf("mesh-ch-%x", []byte(channelName))
	}
	if len(senderKey) >= 8 {
		return fmt.Sprintf("mesh-dm-%x", []byte(senderKey)[:8])
	}
	return "mesh-dm-default"
}
