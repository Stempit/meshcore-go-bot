package poker

import (
	"time"
)

// afterFunc is the timer primitive; tests replace it with a fake that captures
// callbacks so timeouts fire deterministically.
var afterFunc func(time.Duration, func()) *time.Timer = time.AfterFunc

func stopTimer(tm **time.Timer) {
	if *tm != nil {
		(*tm).Stop()
		*tm = nil
	}
}

// armJoin arms the join window (lobby phase).
func armJoin(t *table) {
	stopTimer(&t.joinT)
	d := time.Duration(joinSec()) * time.Second
	t.joinT = afterFunc(d, func() { onJoinWindow(t) })
}

// onJoinWindow fires at the end of a join window: deal with 2+ seated, or
// close a table that never filled.
func onJoinWindow(t *table) {
	mu.Lock()
	if tables[t.channel] != t || t.phase != phaseLobby {
		mu.Unlock()
		return
	}
	lang := pokerLang()
	o := newOut()
	o.chanSet(t.channel)
	if len(t.players) < 2 {
		o.line(lang, "join_window_close")
		closeTable(t)
		mu.Unlock()
		o.flush()
		return
	}
	dealHand(t, lang, o)
	mu.Unlock()
	o.flush()
}

// armPause arms the between-hands deal-pause window.
func armPause(t *table) {
	stopTimer(&t.pauseT)
	d := time.Duration(dealPauseSec()) * time.Second
	t.pauseT = afterFunc(d, func() { onDealPause(t) })
}

// onDealPause fires at the end of the deal-pause window and starts the next
// hand (dealHand itself parks a starved table back in the lobby).
func onDealPause(t *table) {
	mu.Lock()
	if tables[t.channel] != t || t.phase != phasePause {
		mu.Unlock()
		return
	}
	lang := pokerLang()
	o := newOut()
	o.chanSet(t.channel)
	dealHand(t, lang, o)
	mu.Unlock()
	o.flush()
}

// armTurn arms the turn timeout for the seat on the clock, capturing the
// handID/turnSeq guard against stale fires.
func armTurn(t *table) {
	stopTimer(&t.turnT)
	if t.phase != phaseHand || t.toAct < 0 {
		return
	}
	hand, seq := t.handID, t.turnSeq
	d := time.Duration(turnTimeoutSec()) * time.Second
	t.turnT = afterFunc(d, func() { onTurnTimeout(t, hand, seq) })
}

// onTurnTimeout applies the auto action (check when free, else fold) for a
// seat whose clock expired.
func onTurnTimeout(t *table, hand, seq uint64) {
	mu.Lock()
	if tables[t.channel] != t || t.phase != phaseHand || t.handID != hand || t.turnSeq != seq || t.toAct < 0 {
		mu.Unlock()
		return
	}
	lang := pokerLang()
	o := newOut()
	o.chanSet(t.channel)
	t.autoAct(lang, o)
	mu.Unlock()
	o.flush()
}

// armIdle (re)arms the table idle close; every table interaction resets it.
func armIdle(t *table) {
	stopTimer(&t.idleT)
	d := time.Duration(idleTimeoutSec()) * time.Second
	t.idleT = afterFunc(d, func() {
		mu.Lock()
		if tables[t.channel] != t {
			mu.Unlock()
			return
		}
		lang := pokerLang()
		o := newOut()
		o.chanSet(t.channel)
		o.line(lang, "idle_closed")
		closeTable(t)
		mu.Unlock()
		o.flush()
	})
}
