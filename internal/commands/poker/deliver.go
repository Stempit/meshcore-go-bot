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

// outMsg is one queued announcement: a channel dealer line or a DM to a
// player's rawKey.
type outMsg struct {
	dm   bool
	raw  string
	text string
}

// out collects one poker turn's outputs: the direct reply (returned through
// Handle) plus an ordered announcement list. The list order IS the delivery
// order over the serialized radio sender: the acting player's confirmation
// must be queued before the channel dealer line and the next player's turn
// prompt, and deal DMs must go out in seat order — a map here once randomized
// the per-player order. Everything is buffered while the engine lock is held
// and actually sent by flush, after the lock is released.
type out struct {
	chanScope string
	msgs      []outMsg
	replyMsgs []string
}

func newOut() *out { return &out{} }

func (o *out) reply(lang, key string, args ...any) {
	o.replyRaw(tr.T(lang, key, args...))
}

func (o *out) replyRaw(text string) { o.replyMsgs = append(o.replyMsgs, text) }

// chanSet targets the dealer commentary at the table's channel.
func (o *out) chanSet(channel string) { o.chanScope = channel }

func (o *out) line(lang, key string, args ...any) {
	o.chanRaw(tr.T(lang, key, args...))
}

// chanRaw buffers a prebuilt channel line (the flavor win templates vary in
// arg order across languages, so they are Sprintf'd at the call site).
func (o *out) chanRaw(text string) {
	if o.chanScope == "" {
		return
	}
	o.msgs = append(o.msgs, outMsg{text: text})
}

// dm renders and buffers a DM to a player; every DM also lands in the
// player's personal log ring.
func (o *out) dm(s *seat, lang, key string, args ...any) {
	o.dmRaw(s, tr.T(lang, key, args...))
}

func (o *out) dmRaw(s *seat, text string) {
	s.pushLog(text)
	o.msgs = append(o.msgs, outMsg{dm: true, raw: s.rawKey, text: text})
}

// flush sends every buffered announcement in order via games.Announce (the
// injected announcer enqueues without blocking).
func (o *out) flush() {
	for _, m := range o.msgs {
		if m.dm {
			games.Announce("dm:"+m.raw, []string{m.text})
			continue
		}
		if o.chanScope != "" {
			games.Announce(o.chanScope, []string{m.text})
		}
	}
	o.msgs = nil
}

// chanTexts returns the queued channel lines in delivery order (tests).
func (o *out) chanTexts() []string {
	var ts []string
	for _, m := range o.msgs {
		if !m.dm {
			ts = append(ts, m.text)
		}
	}
	return ts
}

// dmTexts returns the queued DM texts for raw in delivery order (tests).
func (o *out) dmTexts(raw string) []string {
	var ts []string
	for _, m := range o.msgs {
		if m.dm && m.raw == raw {
			ts = append(ts, m.text)
		}
	}
	return ts
}

// dmTargets counts distinct DM recipients queued so far (tests).
func (o *out) dmTargets() int {
	seen := map[string]bool{}
	for _, m := range o.msgs {
		if m.dm {
			seen[m.raw] = true
		}
	}
	return len(seen)
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
