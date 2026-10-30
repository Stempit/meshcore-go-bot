// Package poker implements .poker: a fixed-limit Texas Hold'em table played
// on a public MeshCore channel. The channel carries the dealer commentary
// (deals, betting news, streets, results); every betting action happens in DMs
// with the bot, where players also receive their hole cards and a personal
// event log. State is in-memory and lost on restart (repo convention).
//
// This file holds the game engine (tables, seats, betting, showdown). All
// engine functions assume the caller holds mu and report through an *out
// collector; timers live in timers.go and rendering in deliver.go.
package poker

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stempit/meshcore-go-bot/internal/support/cards"
	"github.com/stempit/meshcore-go-bot/internal/support/games"
	"github.com/stempit/meshcore-go-bot/internal/support/helpers"
)

// shuffleDeck is the deck shuffler; tests swap it for a deterministic no-op.
var shuffleDeck = cards.Shuffle

type phase uint8

const (
	phaseLobby phase = iota // join window (or standing lobby after a starved table)
	phasePause              // deal-pause window between hands
	phaseHand               // a hand is running
	phaseOver
)

type streetNum uint8

const (
	preflop streetNum = iota
	flop
	turn
	river
)

const logCap = 10 // per-player personal event ring buffer

// strikeOut is the consecutive auto-actions (auto-check or auto-fold, across
// hands) that remove a player from the table: someone who misses two turns
// in a row is gone or not playing.
const strikeOut = 2

// seat is one player, keyed by the lowercase-hex public key (the canonical
// identity across the channel and DM paths). rawKey is the raw public-key
// bytes, used only to address DM announcements ("dm:"+rawKey).
type seat struct {
	keyHex string
	rawKey string
	name   string

	stack int

	dealt     bool
	hole      [2]cards.Card
	contrib   int // total chips put in this hand (the side-pot key)
	streetBet int // chips put in the current street
	folded    bool
	allin     bool

	leaving  bool // exits at the end of the current hand
	timeouts int  // consecutive turn timeouts (reset by any real action)
	log      []string
}

func (s *seat) pushLog(text string) {
	s.log = append(s.log, text)
	if len(s.log) > logCap {
		s.log = s.log[len(s.log)-logCap:]
	}
}

// table is one channel's table. All fields are guarded by mu.
type table struct {
	channel   string
	openerHex string

	players []*seat // seated, in seat order; button indexes here
	waiting []*seat // joiners queued for the next deal
	button  int

	phase phase

	street   streetNum
	board    []cards.Card
	deck     []cards.Card
	curBet   int
	raiseCnt int // raises this street (the opening bet does not count)
	pending  map[int]bool
	toAct    int // seat index whose turn it is; -1 between streets
	handID   uint64
	turnSeq  uint64 // bumped per prompted turn; the stale-timer guard

	joinT, pauseT, turnT, idleT *time.Timer // armed in timers.go, nil when stopped
}

var (
	mu     sync.Mutex
	tables = map[string]*table{} // keyed by channel name
)

// locate finds the table where keyHex sits (players or waiting queue) plus
// whether the seat is still waiting for the next deal.
func locate(keyHex string) (*table, *seat, bool) {
	if keyHex == "" {
		return nil, nil, false
	}
	for _, t := range tables {
		for _, s := range t.players {
			if s.keyHex == keyHex {
				return t, s, false
			}
		}
		for _, s := range t.waiting {
			if s.keyHex == keyHex {
				return t, s, true
			}
		}
	}
	return nil, nil, false
}

func (t *table) nextIdx(i int) int { return (i + 1) % len(t.players) }

func (t *table) betSize() int {
	if t.street <= flop {
		return smallBet()
	}
	return bigBet()
}

func (t *table) potTotal() int {
	n := 0
	for _, s := range t.players {
		n += s.contrib
	}
	return n
}

func (t *table) liveSeats() []int {
	var out []int
	for i, s := range t.players {
		if s.dealt && !s.folded {
			out = append(out, i)
		}
	}
	return out
}

// actable counts players who can still make betting actions.
func (t *table) actable() int {
	n := 0
	for _, s := range t.players {
		if s.dealt && !s.folded && !s.allin {
			n++
		}
	}
	return n
}

// canRaise reports whether seat si may make a full raise now: the raise cap is
// not reached and the stack covers both the call and the full increment.
func (t *table) canRaise(si int) bool {
	s := t.players[si]
	if t.raiseCnt >= maxRaises() {
		return false
	}
	facing := t.curBet - s.streetBet
	if s.stack <= facing {
		return false
	}
	return s.stack >= t.curBet+t.betSize()-s.streetBet
}

func (t *table) initPending() {
	t.pending = map[int]bool{}
	for i, s := range t.players {
		if s.dealt && !s.folded && !s.allin {
			t.pending[i] = true
		}
	}
}

// reopen resets the pending set to everyone but si (after their raise).
func (t *table) reopen(si int) {
	t.pending = map[int]bool{}
	for i, s := range t.players {
		if i != si && s.dealt && !s.folded && !s.allin {
			t.pending[i] = true
		}
	}
}

func (t *table) postBlind(i, amount int) {
	s := t.players[i]
	pay := min(amount, s.stack)
	s.stack -= pay
	s.streetBet += pay
	s.contrib += pay
	if s.stack == 0 {
		s.allin = true
	}
}

// ---- table management (channel verbs) ----

// chStart opens a table; the opener seats immediately (they must be a known
// contact, same gate as join). Caller holds mu.
func chStart(lang, channel, keyHex, rawKey, name string, o *out) {
	if tables[channel] != nil {
		o.reply(lang, "already_open")
		return
	}
	if keyHex == "" {
		o.reply(lang, "join_fail_advert")
		return
	}
	t := &table{channel: channel, openerHex: keyHex, toAct: -1, pending: map[int]bool{}}
	tables[channel] = t
	o.chanSet(channel)
	if !trySeat(t, lang, keyHex, rawKey, name, o) {
		delete(tables, channel) // opener failed the gate: no table
		return
	}
	o.line(lang, "table_open", joinSec())
	armJoin(t)
	armIdle(t)
}

// chJoin seats or queues the sender at the channel's table. Caller holds mu.
func chJoin(lang, channel, keyHex, rawKey, name string, o *out) {
	t := tables[channel]
	if t == nil {
		o.reply(lang, "no_table")
		return
	}
	o.chanSet(channel)
	trySeat(t, lang, keyHex, rawKey, name, o)
}

// trySeat seats a player (or queues them during a hand) and handles every
// failure reply. Caller holds mu.
func trySeat(t *table, lang, keyHex, rawKey, name string, o *out) bool {
	if keyHex == "" {
		o.reply(lang, "join_fail_advert")
		return false
	}
	if ot, _, _ := locate(keyHex); ot != nil {
		o.reply(lang, "already_seated", ot.channel)
		return false
	}
	if len(t.players)+len(t.waiting) >= maxPlayers() {
		o.reply(lang, "table_full", maxPlayers())
		return false
	}
	s := &seat{
		keyHex: keyHex,
		rawKey: rawKey,
		name:   helpers.ClipRunes(games.Who(name), games.NameMaxRune),
		stack:  startingStack(),
	}
	if t.phase == phaseHand {
		t.waiting = append(t.waiting, s)
		o.reply(lang, "join_ok", len(t.players)+len(t.waiting), maxPlayers())
		o.dm(s, lang, "welcome_dm")
		armIdle(t)
		return true
	}
	t.players = append(t.players, s)
	o.reply(lang, "join_ok", len(t.players)+len(t.waiting), maxPlayers())
	o.dm(s, lang, "welcome_dm")
	armIdle(t)
	if t.phase == phaseLobby && t.joinT == nil && len(t.players) >= 2 {
		armJoin(t) // standing table refills: fresh join window
	}
	if len(t.players) >= maxPlayers() {
		dealHand(t, lang, o) // full table deals immediately
	}
	return true
}

// chLeave honors .poker leave. Caller holds mu.
func chLeave(lang, channel, keyHex string, o *out) {
	t := tables[channel]
	if t == nil {
		o.reply(lang, "no_table")
		return
	}
	o.chanSet(channel)
	lt, s, waiting := locate(keyHex)
	if lt != t || s == nil {
		o.reply(lang, "no_table")
		return
	}
	switch {
	case waiting:
		t.waiting = removeSeat(t.waiting, s)
		o.reply(lang, "leave_ok")
	case t.phase == phaseHand:
		s.leaving = true // exits at the end of the current hand
		o.reply(lang, "leave_ok")
	default:
		t.players = removeSeat(t.players, s)
		o.reply(lang, "leave_ok")
		if len(t.players) == 0 && len(t.waiting) == 0 {
			o.line(lang, "table_closed")
			closeTable(t)
		}
	}
}

// chEnd implements .poker end: only the opener may close the table (when the
// opener is no longer seated, any seated player may); a seated non-opener
// running it is equivalent to leave; bystanders are refused. Caller holds mu.
func chEnd(lang, channel, keyHex string, o *out) {
	t := tables[channel]
	if t == nil {
		o.reply(lang, "no_table")
		return
	}
	o.chanSet(channel)

	openerSeated := false
	senderSeated := false
	for _, s := range t.players {
		if s.keyHex == t.openerHex {
			openerSeated = true
		}
		if s.keyHex == keyHex {
			senderSeated = true
		}
	}
	for _, s := range t.waiting {
		if s.keyHex == t.openerHex {
			openerSeated = true
		}
	}

	if keyHex == t.openerHex || (!openerSeated && senderSeated) {
		if t.phase == phaseHand {
			for _, s := range t.players { // abandoned hand: chips go home
				s.stack += s.contrib
				s.contrib = 0
			}
		}
		o.line(lang, "table_closed")
		closeTable(t)
		return
	}
	if senderSeated {
		chLeave(lang, channel, keyHex, o)
		return
	}
	o.reply(lang, "end_denied")
}

func removeSeat(seats []*seat, s *seat) []*seat {
	for i, x := range seats {
		if x == s {
			return append(seats[:i], seats[i+1:]...)
		}
	}
	return seats
}

func closeTable(t *table) {
	stopTimer(&t.joinT)
	stopTimer(&t.pauseT)
	stopTimer(&t.turnT)
	stopTimer(&t.idleT)
	t.phase = phaseOver
	t.toAct = -1
	delete(tables, t.channel)
}

// ---- hand lifecycle ----

// dealHand seats queued joiners and starts the next hand, or parks a starved
// table back in the lobby. Caller holds mu.
func dealHand(t *table, lang string, o *out) {
	o.chanSet(t.channel)
	stopTimer(&t.joinT)
	stopTimer(&t.pauseT)
	if len(t.players) < 2 {
		t.phase = phaseLobby
		o.line(lang, "lobby_wait")
		armIdle(t)
		return
	}
	t.phase = phaseHand
	t.handID++
	t.turnSeq = 0
	t.board = nil
	t.street = preflop
	t.deck = cards.Deck()
	shuffleDeck(t.deck)
	for _, s := range t.players {
		s.dealt, s.folded, s.allin = true, false, false
		s.contrib, s.streetBet = 0, 0
	}

	n := len(t.players)
	var sbIdx, bbIdx, first int
	if n == 2 { // heads-up: the button posts SB and acts first preflop
		sbIdx, bbIdx = t.button, t.nextIdx(t.button)
		first = t.button
	} else {
		sbIdx = t.nextIdx(t.button)
		bbIdx = t.nextIdx(sbIdx)
		first = t.nextIdx(bbIdx) // UTG, left of the big blind
	}
	t.postBlind(sbIdx, blindSB())
	t.postBlind(bbIdx, blindBB())
	t.curBet = blindBB()
	t.raiseCnt = 0
	for round := 0; round < 2; round++ {
		for k := 1; k <= n; k++ {
			i := (t.button + k) % n
			t.players[i].hole[round] = t.deck[0]
			t.deck = t.deck[1:]
		}
	}

	names := make([]string, 0, n)
	for _, s := range t.players {
		names = append(names, s.name)
	}
	o.line(lang, "deal", strings.Join(names, ", "), blindSB(), blindBB())
	for _, s := range t.players {
		o.dm(s, lang, "deal_dm", cards.Render(s.hole[:]), s.stack, blindSB(), blindBB())
	}

	t.initPending()
	t.toAct = (first + n - 1) % n // advance() prompts `first`
	advance(t, lang, o)
}

// advance drives the table forward after any hand-state change until it either
// waits on a player action or the hand ends (folds, runout, showdown).
// Caller holds mu.
func advance(t *table, lang string, o *out) {
	n := len(t.players)
	for {
		if len(t.liveSeats()) <= 1 {
			endHand(t, lang, o)
			return
		}
		if len(t.pending) == 0 {
			if t.street == river {
				endHand(t, lang, o)
				return
			}
			t.street++
			openStreet(t, lang, o)
			if t.actable() < 2 {
				continue // all-in runout: keep dealing, nobody can bet
			}
			t.initPending()
			first := t.nextIdx(t.button) // postflop order: left of the button
			if promptNext(t, (first+n-1)%n, lang, o) {
				return
			}
			continue
		}
		if promptNext(t, t.toAct, lang, o) {
			return
		}
		t.pending = map[int]bool{} // defensive: unpromptable pending, force street end
	}
}

// promptNext prompts the next pending seat after `after` (wrapping). Returns
// false when nobody is pending. Caller holds mu.
func promptNext(t *table, after int, lang string, o *out) bool {
	n := len(t.players)
	for k := 1; k <= n; k++ {
		i := (after + k) % n
		if t.pending[i] {
			t.toAct = i
			t.turnSeq++
			o.turnDM(t, i, lang)
			armTurn(t)
			return true
		}
	}
	return false
}

func openStreet(t *table, lang string, o *out) {
	t.curBet = 0
	t.raiseCnt = 0
	for _, s := range t.players {
		s.streetBet = 0
	}
	switch t.street {
	case flop:
		t.board = append(t.board, t.deck[0], t.deck[1], t.deck[2])
		t.deck = t.deck[3:]
	case turn, river:
		t.board = append(t.board, t.deck[0])
		t.deck = t.deck[1:]
	}
	o.line(lang, streetKey(t.street), cards.Render(t.board), t.potTotal())
}

// endHand settles the current hand: uncalled-bet refund, showdown or
// last-live award, pot splitting with side pots and the odd-chip rule, then
// postHand bookkeeping. Caller holds mu.
func endHand(t *table, lang string, o *out) {
	o.chanSet(t.channel)
	stopTimer(&t.turnT)
	t.toAct = -1
	t.pending = map[int]bool{}

	contribs := make([]int, len(t.players))
	live := make([]bool, len(t.players))
	liveIdx := []int{}
	for i, s := range t.players {
		contribs[i] = s.contrib
		live[i] = s.dealt && !s.folded
		if live[i] {
			liveIdx = append(liveIdx, i)
		}
	}
	for i, r := range cards.RefundUncalled(contribs) {
		if r > 0 {
			t.players[i].stack += r
			t.players[i].contrib -= r
			contribs[i] -= r
		}
	}

	if len(liveIdx) == 1 {
		w := liveIdx[0]
		amount := t.potTotal()
		t.players[w].stack += amount
		o.chanRaw(fmt.Sprintf(pick(tr.TList(lang, "win")), t.players[w].name, amount, cards.Render(t.players[w].hole[:])))
	} else {
		scores := make(map[int]cards.Score, len(liveIdx))
		for _, i := range liveIdx {
			var seven [7]cards.Card
			copy(seven[:2], t.players[i].hole[:])
			copy(seven[2:], t.board)
			sc, _ := cards.Best7(seven)
			scores[i] = sc
		}
		entries := make([]string, 0, len(liveIdx))
		for _, i := range liveIdx {
			entries = append(entries, tr.T(lang, "showdown.entry", t.players[i].name, handName(lang, scores[i])))
		}
		o.line(lang, "showdown", strings.Join(entries, "; "))

		for _, pot := range cards.SplitPots(contribs, live) {
			var best cards.Score
			for k, i := range pot.Eligible {
				if k == 0 || cards.Compare(scores[i], best) > 0 {
					best = scores[i]
				}
			}
			winners := []int{}
			for _, i := range pot.Eligible {
				if cards.Compare(scores[i], best) == 0 {
					winners = append(winners, i)
				}
			}
			orderFromButton(winners, t.button, len(t.players))
			share := pot.Amount / len(winners)
			rem := pot.Amount % len(winners)
			for k, w := range winners {
				gain := share
				if k < rem {
					gain++ // odd chip: first winner left of the button
				}
				t.players[w].stack += gain
			}
			if len(winners) == 1 {
				w0 := t.players[winners[0]]
				o.chanRaw(fmt.Sprintf(pick(tr.TList(lang, "win")), w0.name, pot.Amount, cards.Render(w0.hole[:])))
			} else {
				names := make([]string, 0, len(winners))
				for _, w := range winners {
					names = append(names, t.players[w].name+" "+cards.Render(t.players[w].hole[:]))
				}
				o.line(lang, "split", strings.Join(names, ", "), share)
			}
		}
	}
	o.line(lang, "stacks", stacksLine(t))
	postHand(t, lang, o)
}

// orderFromButton sorts seat indexes by their distance after the button
// (ascending, the button itself last) in place — the split-pot payout order,
// which also decides who gets the odd chip.
func orderFromButton(idx []int, button, n int) {
	dist := func(i int) int {
		d := (i - button + n) % n
		if d == 0 {
			d = n
		}
		return d
	}
	for i := 1; i < len(idx); i++ {
		for j := i; j > 0 && dist(idx[j]) < dist(idx[j-1]); j-- {
			idx[j], idx[j-1] = idx[j-1], idx[j]
		}
	}
}

func stacksLine(t *table) string {
	parts := make([]string, 0, len(t.players))
	for _, s := range t.players {
		parts = append(parts, s.name+" "+strconv.Itoa(s.stack))
	}
	return strings.Join(parts, ", ")
}

// postHand removes timed-out, busted, and leaving seats, rotates the button,
// and either declares a table victory, parks a starved table in the lobby, or
// starts the deal-pause window for the next hand. Caller holds mu.
func postHand(t *table, lang string, o *out) {
	old := t.players
	oldBtn := t.button

	var keep []*seat
	busted := 0
	for _, s := range old {
		switch {
		case s.timeouts >= strikeOut:
			o.line(lang, "timeout.out", s.name)
		case s.stack <= 0:
			busted++
			o.line(lang, "elim", s.name)
		case s.leaving:
			// silent exit after the hand
		default:
			keep = append(keep, s)
		}
	}
	t.players = keep

	if len(keep) > 0 { // rotate the button to the next kept seat
		n := len(old)
		t.button = 0
	nextSeat:
		for k := 1; k <= n; k++ {
			cand := old[(oldBtn+k)%n]
			for j, s := range keep {
				if s == cand {
					t.button = j
					break nextSeat
				}
			}
		}
	}

	switch {
	case len(keep) == 0:
		o.line(lang, "table_closed")
		closeTable(t)
		return
	case len(keep) == 1 && busted > 0:
		w := keep[0]
		games.LogWin(winLog, t.channel, w.name, "holdem")
		o.line(lang, "win_table", w.name)
		closeTable(t)
		return
	case len(keep) < 2:
		t.phase = phaseLobby
		o.line(lang, "lobby_wait")
		armIdle(t)
		return
	}

	t.phase = phasePause
	if len(t.waiting) > 0 {
		t.players = append(t.players, t.waiting...)
		t.waiting = nil
	}
	o.line(lang, "deal_pause", dealPauseSec())
	armPause(t)
	armIdle(t)
}

// ---- player actions (DM verbs) ----

// dmAction applies a parsed action verb from the seat keyed keyHex.
// Caller holds mu.
func dmAction(lang, keyHex, verb string, o *out) {
	t, s, waiting := locate(keyHex)
	if t == nil || waiting {
		o.reply(lang, "not_in_hand")
		return
	}
	o.chanSet(t.channel)
	if t.phase != phaseHand || !s.dealt {
		o.reply(lang, "not_in_hand")
		return
	}
	if s.folded {
		o.reply(lang, "folded_already")
		return
	}
	si := -1
	for i, p := range t.players {
		if p == s {
			si = i
			break
		}
	}
	if si != t.toAct {
		who := ""
		if t.toAct >= 0 {
			who = t.players[t.toAct].name
		}
		o.reply(lang, "not_your_turn", who)
		return
	}

	facing := t.curBet - s.streetBet
	switch verb {
	case "fold":
		t.actFold(si, lang, o)
	case "check":
		if facing > 0 {
			o.reply(lang, "check_denied", min(facing, s.stack))
			return
		}
		t.actCheck(si, lang, o)
	case "call":
		if facing <= 0 {
			t.actCheck(si, lang, o)
		} else {
			t.actCall(si, lang, o)
		}
	case "bet", "raise":
		if !t.canRaise(si) {
			if facing > 0 {
				o.reply(lang, "raise_denied_bet", min(facing, s.stack))
			} else {
				o.reply(lang, "raise_denied_free")
			}
			return
		}
		t.actRaise(si, lang, o)
	case "allin":
		t.actAllin(si, lang, o)
	default:
		o.reply(lang, "usage")
		return
	}
	armIdle(t)
	advance(t, lang, o)
}

func (t *table) actCheck(si int, lang string, o *out) {
	s := t.players[si]
	delete(t.pending, si)
	s.timeouts = 0
	o.dmRaw(s, tr.T(lang, "confirm.check"))
	o.line(lang, "act.check", s.name)
}

func (t *table) actFold(si int, lang string, o *out) {
	s := t.players[si]
	s.folded = true
	delete(t.pending, si)
	s.timeouts = 0
	o.dmRaw(s, tr.T(lang, "confirm.fold"))
	o.line(lang, "act.fold", s.name)
}

func (t *table) actCall(si int, lang string, o *out) {
	s := t.players[si]
	facing := t.curBet - s.streetBet
	pay := min(facing, s.stack)
	s.stack -= pay
	s.streetBet += pay
	s.contrib += pay
	if s.stack == 0 {
		s.allin = true
	}
	delete(t.pending, si)
	s.timeouts = 0
	if s.allin {
		o.dmRaw(s, tr.T(lang, "confirm.allin", pay))
		o.line(lang, "act.allin", s.name, s.streetBet, t.potTotal())
	} else {
		o.dmRaw(s, tr.T(lang, "confirm.call", pay))
		o.line(lang, "act.call", s.name, pay, t.potTotal())
	}
}

func (t *table) actRaise(si int, lang string, o *out) {
	s := t.players[si]
	target := t.curBet + t.betSize()
	cost := target - s.streetBet
	s.stack -= cost
	s.streetBet = target
	s.contrib += cost
	if s.stack == 0 {
		s.allin = true
	}
	t.curBet = target
	t.raiseCnt++
	t.reopen(si)
	s.timeouts = 0
	o.dmRaw(s, tr.T(lang, "confirm.raise", target))
	o.line(lang, "act.raise", s.name, target, t.potTotal())
}

func (t *table) actAllin(si int, lang string, o *out) {
	s := t.players[si]
	pay := s.stack
	s.stack = 0
	s.streetBet += pay
	s.contrib += pay
	s.allin = true
	raised := s.streetBet > t.curBet
	if raised {
		t.curBet = s.streetBet
		t.raiseCnt++
		t.reopen(si)
	} else {
		delete(t.pending, si)
	}
	s.timeouts = 0
	o.dmRaw(s, tr.T(lang, "confirm.allin", s.streetBet))
	o.line(lang, "act.allin", s.name, s.streetBet, t.potTotal())
}

// autoAct applies the timeout action for the seat on the clock: check when
// free, fold otherwise, bumping the consecutive-timeout counter. Caller holds
// mu.
func (t *table) autoAct(lang string, o *out) {
	o.chanSet(t.channel)
	if t.phase != phaseHand || t.toAct < 0 || t.toAct >= len(t.players) {
		return
	}
	si := t.toAct
	s := t.players[si]
	s.timeouts++
	facing := t.curBet - s.streetBet
	if facing <= 0 {
		delete(t.pending, si)
		o.line(lang, "timeout.check", s.name)
		o.dm(s, lang, "timeout.check", s.name)
	} else {
		s.folded = true
		delete(t.pending, si)
		o.line(lang, "timeout.fold", s.name, s.timeouts)
		o.dm(s, lang, "timeout.fold", s.name, s.timeouts)
	}
	armIdle(t)
	advance(t, lang, o)
}
