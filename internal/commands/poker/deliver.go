package poker

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/stempit/meshcore-go-bot/internal/support/cards"
	"github.com/stempit/meshcore-go-bot/internal/support/games"
)

// wireBudget is the byte budget every single-line template must respect; the
// send layer's shrink.Fit is the last-resort splitter for the few legit
// multi-line payloads (stacks, showdown).
const wireBudget = 130

// out collects one poker turn's outputs: the direct reply (returned through
// Handle), channel dealer lines, and player DMs. Everything is buffered while
// the engine lock is held and actually sent by flush, after the lock is
// released (games.Announce enqueues without blocking).
type out struct {
	chanScope string
	chanMsgs  []string
	dms       map[string][]string // rawKey -> messages
	replyMsgs []string
}

func newOut() *out { return &out{dms: map[string][]string{}} }

func (o *out) reply(lang, key string, args ...any) {
	o.replyRaw(tr.T(lang, key, args...))
}

func (o *out) replyRaw(text string) { o.replyMsgs = append(o.replyMsgs, text) }

// chanSet targets the dealer commentary at the table's channel.
func (o *out) chanSet(channel string) { o.chanScope = channel }

func (o *out) line(lang, key string, args ...any) {
	if o.chanScope == "" {
		return
	}
	o.chanMsgs = append(o.chanMsgs, tr.T(lang, key, args...))
}

// chanRaw buffers a prebuilt channel line (the flavor win templates vary in
// arg order across languages, so they are Sprintf'd at the call site).
func (o *out) chanRaw(text string) {
	if o.chanScope == "" {
		return
	}
	o.chanMsgs = append(o.chanMsgs, text)
}

// dm renders and buffers a DM to a player; every DM also lands in the
// player's personal log ring.
func (o *out) dm(s *seat, lang, key string, args ...any) {
	o.dmRaw(s, tr.T(lang, key, args...))
}

func (o *out) dmRaw(s *seat, text string) {
	s.pushLog(text)
	o.dms[s.rawKey] = append(o.dms[s.rawKey], text)
}

func (o *out) flush() {
	if o.chanScope != "" && len(o.chanMsgs) > 0 {
		games.Announce(o.chanScope, o.chanMsgs)
	}
	for raw, msgs := range o.dms {
		games.Announce("dm:"+raw, msgs)
	}
}

// pick returns a random template from a TList pool (dealer flavor lines).
func pick(pool []string) string { return pool[rand.Intn(len(pool))] }

// turnDM buffers the priced dynamic turn prompt: only the currently legal
// actions, with absolute prices computed by the engine. Falls back to the
// short header if the long form would exceed the wire budget.
func (o *out) turnDM(t *table, si int, lang string) {
	s := t.players[si]
	hole := cards.Render(s.hole[:])
	pot := t.potTotal()
	acts := buildActions(t, si, lang)
	msg := tr.T(lang, "turn.frame", tr.T(lang, "turn.header"), hole, pot, acts)
	if len(msg) > wireBudget {
		msg = tr.T(lang, "turn.frame", tr.T(lang, "turn.header_short"), hole, pot, acts)
	}
	o.dmRaw(s, msg)
}

// buildActions renders the legal-action menu for seat si: check when free,
// the priced call (with the all-in annotation when short), the raise target
// when a full raise is available, and fold.
func buildActions(t *table, si int, lang string) string {
	s := t.players[si]
	facing := t.curBet - s.streetBet
	var parts []string
	switch {
	case facing <= 0:
		parts = append(parts, tr.T(lang, "lbl.check"))
	case facing >= s.stack:
		parts = append(parts, tr.T(lang, "lbl.call_all", s.stack))
	default:
		parts = append(parts, tr.T(lang, "lbl.call", facing))
	}
	if t.canRaise(si) {
		parts = append(parts, tr.T(lang, "lbl.raise", t.curBet+t.betSize()))
	}
	parts = append(parts, tr.T(lang, "lbl.fold"))
	return strings.Join(parts, " / ")
}

func streetKey(s streetNum) string { return fmt.Sprintf("street.%d", s) }

// streetLabel is the street name plus board, for status lines.
func streetLabel(t *table, lang string) string {
	label := tr.T(lang, streetKey(t.street))
	if len(t.board) > 0 {
		label += " " + cards.Render(t.board)
	}
	return label
}

// handName describes a score with the category name and rank symbols
// ("пара A"); the category vocabulary is pinned, symbols avoid declension.
func handName(lang string, sc cards.Score) string {
	key := fmt.Sprintf("cat.%d", sc.Cat)
	if sc.Cat == cards.TwoPair || sc.Cat == cards.FullHouse {
		return tr.T(lang, key, cards.Rank(sc.Ranks[0]).String(), cards.Rank(sc.Ranks[1]).String())
	}
	return tr.T(lang, key, cards.Rank(sc.Ranks[0]).String())
}

// statusLine renders the spectator one-liner for the table's current phase.
func statusLine(t *table, lang string) string {
	switch t.phase {
	case phaseHand:
		who := ""
		if t.toAct >= 0 && t.toAct < len(t.players) {
			who = t.players[t.toAct].name
		}
		return tr.T(lang, "status.hand", streetLabel(t, lang), t.potTotal(), who)
	case phasePause:
		return tr.T(lang, "status.pause", dealPauseSec(), len(t.players)+len(t.waiting), maxPlayers())
	default:
		return tr.T(lang, "status.lobby", len(t.players)+len(t.waiting), maxPlayers())
	}
}

// dmStatus renders the personal status line for a seated player.
func dmStatus(t *table, s *seat, lang string) string {
	if t.phase == phaseHand && s.dealt && !s.folded {
		who := ""
		if t.toAct >= 0 && t.toAct < len(t.players) {
			who = t.players[t.toAct].name
		}
		return tr.T(lang, "dm_status", streetLabel(t, lang), s.stack, t.potTotal(), who)
	}
	if t.phase == phaseHand {
		return tr.T(lang, "dm_status_out")
	}
	return tr.T(lang, "status.lobby", len(t.players)+len(t.waiting), maxPlayers())
}

// cardsReply re-sends the player's hole cards (plus board) mid-hand.
func cardsReply(t *table, s *seat, lang string) string {
	if t.phase != phaseHand || !s.dealt {
		return tr.T(lang, "no_cards")
	}
	if len(t.board) > 0 {
		return tr.T(lang, "cards.board", cards.Render(s.hole[:]), cards.Render(t.board))
	}
	return tr.T(lang, "cards", cards.Render(s.hole[:]))
}

// logReply renders the player's personal event ring for ".poker log".
func logReply(s *seat, lang string) []string {
	if len(s.log) == 0 {
		return []string{tr.T(lang, "log.empty")}
	}
	return append([]string{tr.T(lang, "log.title")}, s.log...)
}
