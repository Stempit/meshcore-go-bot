package commands

import "strings"

func GenerateReply(messageText string) (reply string) {
	var err error

	switch {
	// llm
	case strings.HasPrefix(strings.ToLower(messageText), ".llm"):
		reply, err = ProcessLLMMessage(messageText)
		if err != nil {
			reply = "хз"
		}
		return
	//ping
	case strings.ToLower(messageText) == "ping",
		strings.ToLower(messageText) == "пинг",
		strings.ToLower(messageText) == ".ping":
		reply, _ = ProcessPingMessage(messageText)
		return reply
	// no command
	default:
		return ""
	}
}
