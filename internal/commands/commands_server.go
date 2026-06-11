package commands

import (
	"meshbot/internal/config"
	"strings"
)

// GenerateReplies routes incoming text to the appropriate command handler and returns
// the messages to send back. Returns nil, nil when no command matches.
//
// senderKey is a unique session identifier (hex pubkey for DMs, sender name for channels).
// senderName is the human-readable author name (may be empty).
func GenerateReplies(text, channelName string, pingMeta *PingMeta, senderKey, senderName string) ([]string, error) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)

	// @[BotName] mention: route to picoclaw.
	if clawCfg := config.Get().Claw; clawCfg.Enabled && clawCfg.BotName != "" {
		mentionPrefix := "@[" + clawCfg.BotName + "]"
		if strings.HasPrefix(lower, strings.ToLower(mentionPrefix)) {
			prompt := strings.TrimSpace(trimmed[len(mentionPrefix):])
			return ProcessClawCommand(".claw "+prompt, senderKey, channelName)
		}
	}

	// BBS mode: intercept all input for users who are in BBS mode.
	if IsBBSMode(senderKey) {
		if lower == ".exit" {
			ExitBBSMode(senderKey)
			return []string{"Left BBS. Bye!"}, nil
		}
		return ProcessBBSCommand(text, senderKey, senderName)
	}

	// Enter BBS mode.
	if lower == ".2ch" {
		EnterBBSMode(senderKey)
		return []string{"Welcome to BBS!\n/hl for help\n.exit to leave"}, nil
	}

	switch {
	case strings.HasPrefix(lower, ".news"):
		return ProcessNewsCommand(text)

	case strings.HasPrefix(lower, ".last"):
		return ProcessLastCommand(text, channelName)

	case strings.HasPrefix(lower, ".llm"):
		reply, err := ProcessLLMMessage(text)
		if err != nil {
			return []string{"хз"}, nil
		}
		return []string{reply}, nil

	case lower == "ping", lower == "пинг", lower == ".ping":
		reply, _ := ProcessPingMessage()
		return []string{reply}, nil

	case lower == ".trace":
		reply, _ := ProcessTraceMessage(pingMeta)
		return []string{reply}, nil

	case lower == ".w", strings.HasPrefix(lower, ".w "):
		return ProcessWeatherCommand(text)

	case lower == ".ticket", strings.HasPrefix(lower, ".ticket "):
		reply, err := ProcessTicketCommand(text, senderKey)
		if err != nil {
			return []string{"ошибка"}, err
		}
		return []string{reply}, nil

	case lower == ".help", strings.HasPrefix(lower, ".help "):
		reply, _ := ProcessHelpCommand(text)
		return []string{reply}, nil

	case strings.HasPrefix(lower, ".claw"):
		return ProcessClawCommand(text, senderKey, channelName)

	case lower == ".path", strings.HasPrefix(lower, ".path "):
		return ProcessPathCommand(trimmed)

	default:
		return nil, nil
	}
}
