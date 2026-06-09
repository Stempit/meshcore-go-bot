package commands

import "strings"

const helpList = ".ping .trace .last .llm .news .w .2ch .claw\n\".help <cmd>\" for details"

const pingHelp = ".ping — random pong\nalso: ping, пинг"
const traceHelp = ".trace — hop count & distance from last ping"
const lastHelp = ".last [n] [channel]\nshow last n msgs (def 3, max 7) in channel"
const llmHelp = ".llm <query>\nask AI, auto-shrunk to <130 chars"
const newsHelp = ".news — list feeds\n.news <feed> [n] [offset] — recent items"
const weatherHelp = ".w [tomorrow] [city]\ncurrent or next-day weather"
const BBSHelp = ".2ch - start BBS/forum mode"
const clawHelp = ".claw <вопрос>\nспросить picoclaw AI агента"

func ProcessHelpCommand(text string) (string, error) {
	arg := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(text), ".help"))
	switch arg {
	case "ping":
		return pingHelp, nil
	case "trace":
		return traceHelp, nil
	case "last":
		return lastHelp, nil
	case "llm":
		return llmHelp, nil
	case "news":
		return newsHelp, nil
	case "w", "weather":
		return weatherHelp, nil
	case "2ch":
		return BBSHelp, nil
	case "claw":
		return clawHelp, nil
	default:
		return helpList, nil
	}
}
