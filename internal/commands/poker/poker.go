// Plugin registration and message routing for .poker. The channel carries
// management verbs (.poker start/join/leave/end/top and the bare status
// line); betting actions arrive as DMs (bare "call", ".call", ".poker call")
// and are intercepted by the Matcher for seated players only.
package poker

import (
	"embed"
	"encoding/hex"
	"strings"

	"github.com/stempit/meshcore-go-bot/internal/config"
	"github.com/stempit/meshcore-go-bot/internal/support/games"
	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

// winLog records table victories for the ".poker top" leaderboard.
var winLog = games.NewLog("poker")

// pokerLang resolves the global bot language for code paths without a Ctx
// (the timers fire outside any request) — the vote pattern.
func pokerLang() string {
	if c := config.Get(); c != nil && c.Language != "" {
		return c.Language
	}
	return i18n.Base
}

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "poker",
		Triggers:  []string{".poker"},
		Match:     registry.Prefix,
		HelpShort: "Hold'em poker (fixed limit)",
		HelpLong:  ".poker start/join — open table & sit\n.poker leave/end/top — table controls\nActions in DM: call / raise / fold",
		Group:     "Games",
	}
}

func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

// parseActionVerb normalizes the pinned DM action forms: "call", ".call",
// ".poker call" (case-insensitive), each with an optional trailing amount
// that is accepted and ignored. Returns the verb, or ok=false when the text
// is not an action form.
func parseActionVerb(lower string) (string, bool) {
	s := strings.TrimPrefix(lower, ".")
	if rest, ok := strings.CutPrefix(s, "poker "); ok {
		s = rest
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", false
	}
	switch fields[0] {
	case "call", "check", "fold", "bet", "raise", "allin":
		return fields[0], true
	}
	return "", false
}

// keyOf normalizes the two SenderKey flavors into the canonical lowercase-hex
// identity: the channel path already carries hex ("" when the sender is not a
// known contact), the DM path carries the raw public-key bytes.
func keyOf(c registry.Ctx) string {
	if c.IsDM() {
		return hex.EncodeToString([]byte(c.SenderKey))
	}
	return strings.ToLower(c.SenderKey)
}

// rawKeyOf recovers the raw public-key bytes (the DM announce scope).
func rawKeyOf(c registry.Ctx) string {
	if c.IsDM() {
		return c.SenderKey
	}
	raw, err := hex.DecodeString(c.SenderKey)
	if err != nil {
		return ""
	}
	return string(raw)
}

// Matches claims: (a) any ".poker …" message in any context, and (b) DM
// action forms from players seated somewhere (the Matcher replaces Triggers,
// so channel ".poker" verbs must be claimed here too).
func (plugin) Matches(c registry.Ctx) bool {
	if c.Lower == ".poker" || strings.HasPrefix(c.Lower, ".poker ") {
		return true
	}
	if c.IsDM() {
		if _, ok := parseActionVerb(c.Lower); ok {
			mu.Lock()
			defer mu.Unlock()
			t, _, _ := locate(keyOf(c))
			return t != nil
		}
	}
	return false
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	o := newOut()
	var reply []string
	if c.IsDM() {
		reply = handleDM(c, o)
	} else {
		reply = handleChannel(c, o)
	}
	o.flush()
	return reply, nil
}

// verbOf splits the first word after the ".poker" prefix. The prefix is cut
// case-insensitively (Arg is rune-aligned), so ".Poker status" routes like
// ".poker status".
func verbOf(c registry.Ctx) string {
	fields := strings.Fields(c.Arg(".poker"))
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

func handleChannel(c registry.Ctx, o *out) []string {
	mu.Lock()
	defer mu.Unlock()
	lang := c.Lang

	switch verbOf(c) {
	case "":
		t := tables[c.Channel]
		if t == nil {
			o.reply(lang, "discovery")
		} else {
			o.replyRaw(statusLine(t, lang))
		}
	case "start":
		chStart(lang, c.Channel, keyOf(c), rawKeyOf(c), c.SenderName, o)
	case "join":
		chJoin(lang, c.Channel, keyOf(c), rawKeyOf(c), c.SenderName, o)
	case "leave":
		chLeave(lang, c.Channel, keyOf(c), o)
	case "end":
		chEnd(lang, c.Channel, keyOf(c), o)
	case "top":
		if lines := games.Top(games.LogBase("poker"), c.Channel); lines != nil {
			o.replyMsgs = append(o.replyMsgs, lines...)
		} else {
			o.reply(lang, "top_empty")
		}
	case "call", "check", "fold", "bet", "raise", "allin":
		o.reply(lang, "guidance")
	default:
		o.reply(lang, "usage")
	}
	return o.replyMsgs
}

func handleDM(c registry.Ctx, o *out) []string {
	mu.Lock()
	defer mu.Unlock()
	lang := c.Lang

	// Action forms (bare "call", ".call", ".poker call") all normalize through
	// parseActionVerb; Matches guarantees a seated sender for the bare form.
	if verb, ok := parseActionVerb(c.Lower); ok {
		dmAction(lang, keyOf(c), verb, o)
		return o.replyMsgs
	}

	t, s, waiting := locate(keyOf(c))
	switch verbOf(c) {
	case "":
		if t == nil {
			o.reply(lang, "usage")
		} else {
			o.replyRaw(dmStatus(t, s, lang))
		}
	case "cards":
		if t == nil || waiting {
			o.reply(lang, "no_cards")
		} else {
			o.replyRaw(cardsReply(t, s, lang))
		}
	case "log":
		if t == nil || s == nil {
			o.reply(lang, "usage")
		} else {
			o.replyMsgs = append(o.replyMsgs, logReply(s, lang)...)
		}
	case "top":
		if t == nil {
			o.reply(lang, "usage")
		} else {
			if lines := games.Top(games.LogBase("poker"), t.channel); lines != nil {
				o.replyMsgs = append(o.replyMsgs, lines...)
			} else {
				o.reply(lang, "top_empty")
			}
		}
	default:
		o.reply(lang, "usage")
	}
	return o.replyMsgs
}
