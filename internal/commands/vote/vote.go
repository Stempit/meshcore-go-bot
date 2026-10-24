// Package vote answers .vote: a per-channel poll with a question and a list of
// options. Each sender has one changeable, anonymous vote; the poll auto-closes
// on a timer or when anyone runs ".vote end". Channels only.
package vote

import (
	"embed"
	"strings"

	"meshbot/internal/config"
	"meshbot/internal/support/games"
	"meshbot/internal/support/i18n"
	"meshbot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

// voteLang resolves the global bot language for code paths without a Ctx
// (the auto-close timer fires outside any request).
func voteLang() string {
	if c := config.Get(); c != nil && c.Language != "" {
		return c.Language
	}
	return i18n.Base
}

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "vote",
		Triggers:  []string{".vote"},
		Match:     registry.Prefix,
		HelpShort: "group poll",
		HelpLong:  ".vote start question | opt1 | opt2 — poll\n.vote <n> — vote; .vote status / end",
		Group:     "Other",
	}
}

// LocalizedHelp returns the per-language long help for ".vote help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processVote(c.Lang, c.Text, c.SenderKey, c.Channel)
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
func processVote(lang, text, senderKey, channelName string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, ".vote"))
	fields := strings.Fields(rest)

	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	scope := games.Scope(senderKey, channelName)

	switch verb {
	case "":
		return []string{tr.T(lang, "usage")}, nil
	case "start":
		return voteStart(lang, scope, channelName, strings.Join(fields[1:], " "))
	case "status":
		return voteStatus(lang, scope, channelName)
	case "end":
		return voteEnd(lang, scope, channelName)
	default:
		// A numeric first field is a vote cast; anything else is bad input.
		return voteCast(lang, scope, channelName, senderKey, fields[0])
	}
}
