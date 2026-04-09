package commands

import (
	"strings"
)

// GenerateReplies routes incoming text to the appropriate command handler and returns
// the messages to send back. Returns nil, nil when no command matches.
func GenerateReplies(text, channelName string, pingMeta *PingMeta) ([]string, error) {
	lower := strings.ToLower(text)

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

	default:
		return nil, nil
	}
}
