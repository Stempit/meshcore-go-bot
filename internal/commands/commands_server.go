package commands

import (
	"meshbot/internal/config"
	"strings"
)

// GenerateReplies routes incoming text to the appropriate command handler and
// returns the messages to send back plus the command's name (used by the send
// layer to select a mesh-fit variant). Returns nil, "", nil when no command
// matches.
//
// senderKey is a unique session identifier (hex pubkey for DMs, sender name for channels).
// senderName is the human-readable author name (may be empty).
func GenerateReplies(text, channelName string, pingMeta *PingMeta, senderKey, senderName string) ([]string, string, error) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)

	// @[BotName] mention: route to picoclaw.
	if clawCfg := config.Get().Claw; clawCfg.Enabled && clawCfg.BotName != "" {
		mentionPrefix := "@[" + clawCfg.BotName + "]"
		if strings.HasPrefix(lower, strings.ToLower(mentionPrefix)) {
			prompt := strings.TrimSpace(trimmed[len(mentionPrefix):])
			replies, err := ProcessClawCommand(".claw "+prompt, senderKey, channelName)
			return replies, "claw", err
		}
	}

	// BBS mode: intercept all input for users who are in BBS mode.
	if IsBBSMode(senderKey) {
		if lower == ".exit" {
			ExitBBSMode(senderKey)
			return []string{"Left BBS. Bye!"}, "bbs", nil
		}
		replies, err := ProcessBBSCommand(text, senderKey, senderName)
		return replies, "bbs", err
	}

	// Enter BBS mode.
	if lower == ".2ch" {
		EnterBBSMode(senderKey)
		return []string{"Welcome to BBS!\n/hl for help\n.exit to leave"}, "bbs", nil
	}

	switch {
	case strings.HasPrefix(lower, ".news"):
		replies, err := ProcessNewsCommand(text)
		return replies, "news", err

	case strings.HasPrefix(lower, ".last"):
		replies, err := ProcessLastCommand(text, channelName)
		return replies, "last", err

	case strings.HasPrefix(lower, ".llm"):
		reply, err := ProcessLLMMessage(text)
		if err != nil {
			return []string{"хз"}, "llm", nil
		}
		return []string{reply}, "llm", nil

	case lower == "ping", lower == "пинг", lower == ".ping":
		reply, _ := ProcessPingMessage()
		return []string{reply}, "ping", nil

	case lower == ".trace":
		reply, _ := ProcessTraceMessage(pingMeta)
		return []string{reply}, "trace", nil

	case lower == ".w", strings.HasPrefix(lower, ".w "):
		replies, err := ProcessWeatherCommand(text)
		return replies, "weather", err

	case lower == ".ticket", strings.HasPrefix(lower, ".ticket "):
		reply, err := ProcessTicketCommand(text, senderKey)
		if err != nil {
			return []string{"ошибка"}, "ticket", err
		}
		return []string{reply}, "ticket", nil

	case lower == ".help", strings.HasPrefix(lower, ".help "):
		reply, _ := ProcessHelpCommand(text)
		return []string{reply}, "help", nil

	case strings.HasPrefix(lower, ".claw"):
		replies, err := ProcessClawCommand(text, senderKey, channelName)
		return replies, "claw", err

	case lower == ".path", strings.HasPrefix(lower, ".path "):
		replies, err := ProcessPathCommand(trimmed)
		return replies, "path", err

	case strings.HasPrefix(lower, ".8ball"):
		replies, err := ProcessEightBallCommand(text)
		return replies, "8ball", err

	default:
		return nil, "", nil
	}
}
