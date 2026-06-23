// Package vote answers .vote: a per-channel poll with a question and a list of
// options. Each sender has one changeable, anonymous vote; the poll auto-closes
// on a timer or when anyone runs ".vote end". Channels only.
package vote

import (
	"strings"

	"meshbot/internal/support/games"
	"meshbot/internal/support/registry"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "vote",
		Triggers:  []string{".vote"},
		Match:     registry.Prefix,
		HelpShort: "group poll",
		HelpLong:  ".vote start вопрос | вариант1 | вариант2 — опрос\n.vote <номер> — голос; .vote status / end",
		Group:     "Other",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processVote(c.Text, c.SenderKey, c.Channel)
}

// processVote handles ".vote" and its sub-verbs (channel only).
//
//	.vote start вопрос | a | b  – open a poll
//	.vote <номер>               – cast/change this sender's single vote
//	.vote status                – current results + time left
//	.vote end                   – close the poll now and show results
//
// senderKey identifies the voter (one changeable vote each); channelName keys
// the poll. Votes are anonymous: results never name who voted.
func processVote(text, senderKey, channelName string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, ".vote"))
	fields := strings.Fields(rest)

	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	scope := games.Scope(senderKey, channelName)

	switch verb {
	case "":
		return []string{voteUsage}, nil
	case "start":
		return voteStart(scope, channelName, strings.Join(fields[1:], " "))
	case "status":
		return voteStatus(scope, channelName)
	case "end":
		return voteEnd(scope, channelName)
	default:
		// A numeric first field is a vote cast; anything else is bad input.
		return voteCast(scope, channelName, senderKey, fields[0])
	}
}
