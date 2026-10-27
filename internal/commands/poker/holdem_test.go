package poker

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stempit/meshcore-go-bot/internal/support/cards"
	"github.com/stempit/meshcore-go-bot/internal/support/games"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

// setupTest freezes the globals engine tests must control (deterministic deck,
// timers that never fire) and restores them afterwards.
func setupTest(t *testing.T) {
	t.Helper()
	oldShuffle, oldAfter, oldTables, oldConf := shuffleDeck, afterFunc, tables, conf
	shuffleDeck = func([]cards.Card) {} // identity: deck stays sorted
	afterFunc = func(time.Duration, func()) *time.Timer {
		return time.AfterFunc(time.Hour, func() {}) // never fires during tests
	}
	tables = map[string]*table{}
	conf = cfg{}
	t.Cleanup(func() {
		shuffleDeck, afterFunc, tables, conf = oldShuffle, oldAfter, oldTables, oldConf
	})
}

// mkTable builds a seated lobby table with n players; player i has keyHex
// hex("raw<i>") so both the channel (hex) and DM (raw) paths resolve.
func mkTable(n int) *table {
	tb := &table{channel: "chan", openerHex: keyHexFor(0), toAct: -1, pending: map[int]bool{}}
	for i := 0; i < n; i++ {
		tb.players = append(tb.players, &seat{
			keyHex: keyHexFor(i),
			rawKey: rawKeyFor(i),
			name:   fmt.Sprintf("P%d", i),
			stack:  startingStack(),
		})
	}
	tables["chan"] = tb
	return tb
}

func keyHexFor(i int) string { return hex.EncodeToString([]byte(rawKeyFor(i))) }
func rawKeyFor(i int) string { return fmt.Sprintf("raw%d", i) }

// act performs a DM action from player i and returns the output collector.
func act(t *testing.T, tb *table, i int, verb string) *out {
	t.Helper()
	o := newOut()
	dmAction("ru", keyHexFor(i), verb, o)
	return o
}

// riverState forces a synthetic river showdown: given holes, board, and
// contributions (deducted from stacks), with everyone live.
func riverState(tb *table, holes map[int][2]cards.Card, contribs []int, board []cards.Card) {
	tb.phase = phaseHand
	tb.street = river
	tb.board = board
	for i, s := range tb.players {
		s.dealt, s.folded, s.allin = true, false, false
		if h, ok := holes[i]; ok {
			s.hole = h
		}
		if i < len(contribs) {
			s.contrib = contribs[i]
			s.stack -= contribs[i]
		}
	}
}

func card(r uint8, s cards.Suit) cards.Card { return cards.New(cards.Rank(r), s) }

func TestDealThreeHanded(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	o := newOut()
	dealHand(tb, "ru", o)

	if got := tb.players[1].stack; got != 195 {
		t.Errorf("SB stack = %d, want 195", got)
	}
	if got := tb.players[2].stack; got != 190 {
		t.Errorf("BB stack = %d, want 190", got)
	}
	if tb.curBet != 10 || tb.raiseCnt != 0 {
		t.Errorf("curBet=%d raiseCnt=%d, want 10/0", tb.curBet, tb.raiseCnt)
	}
	if tb.toAct != 0 {
		t.Errorf("first to act = P%d, want P0 (UTG)", tb.toAct)
	}
	if len(tb.pending) != 3 {
		t.Errorf("pending = %v, want all three", tb.pending)
	}
	if tb.potTotal() != 15 {
		t.Errorf("pot = %d, want 15", tb.potTotal())
	}
	if o.dmTargets() != 3 || len(o.chanTexts()) == 0 {
		t.Errorf("missing announcements: dmTargets=%d chan=%d", o.dmTargets(), len(o.chanTexts()))
	}
	for _, s := range tb.players {
		if !s.dealt {
			t.Fatalf("player %s not dealt", s.name)
		}
	}
}

func TestDealHeadsUp(t *testing.T) {
	setupTest(t)
	tb := mkTable(2)
	dealHand(tb, "ru", newOut())

	if tb.players[0].stack != 195 || tb.players[1].stack != 190 {
		t.Errorf("heads-up blinds wrong: %d/%d", tb.players[0].stack, tb.players[1].stack)
	}
	if tb.toAct != 0 {
		t.Errorf("first to act = P%d, want the button (SB) P0", tb.toAct)
	}
}

func TestRaiseCapCallAroundFlop(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	dealHand(tb, "ru", newOut())

	act(t, tb, 0, "raise") // to 20
	if tb.curBet != 20 || tb.players[0].stack != 180 || tb.raiseCnt != 1 {
		t.Fatalf("after first raise: curBet=%d stack=%d raises=%d", tb.curBet, tb.players[0].stack, tb.raiseCnt)
	}
	act(t, tb, 1, "raise") // to 30
	act(t, tb, 2, "raise") // to 40 — the cap

	o := act(t, tb, 0, "raise") // fourth raise: denied
	if len(o.replyMsgs) != 1 || !strings.Contains(o.replyMsgs[0], "call 20") {
		t.Fatalf("cap denial reply = %v", o.replyMsgs)
	}
	if tb.players[0].stack != 180 {
		t.Errorf("denied raise must not move chips: stack=%d", tb.players[0].stack)
	}

	act(t, tb, 0, "call") // 20 more, to 40
	act(t, tb, 1, "call") // 10 more, to 40; the last aggressor P2 does not act again

	if tb.street != flop || len(tb.board) != 3 {
		t.Fatalf("street=%d board=%d, want flop with 3 cards", tb.street, len(tb.board))
	}
	if tb.curBet != 0 || tb.raiseCnt != 0 || tb.players[2].streetBet != 0 {
		t.Errorf("street not reset: curBet=%d raiseCnt=%d sb2=%d", tb.curBet, tb.raiseCnt, tb.players[2].streetBet)
	}
	if tb.toAct != 1 {
		t.Errorf("flop first to act = P%d, want P1 (left of button)", tb.toAct)
	}
	if tb.potTotal() != 120 {
		t.Errorf("pot = %d, want 120", tb.potTotal())
	}
	for i, want := range []int{160, 160, 160} {
		if got := tb.players[i].stack; got != want {
			t.Errorf("P%d stack = %d, want %d", i, got, want)
		}
	}
}

func TestEveryoneFoldsRefundsUncalled(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	dealHand(tb, "ru", newOut())

	act(t, tb, 0, "raise") // to 20
	act(t, tb, 1, "fold")
	o := act(t, tb, 2, "fold")

	if tb.phase != phasePause {
		t.Fatalf("phase = %d, want pause", tb.phase)
	}
	// P0's raise above the BB was uncalled and refunded; P0 wins the 25 pot.
	if got := tb.players[0].stack; got != 215 {
		t.Errorf("winner stack = %d, want 215", got)
	}
	found, foundCards := false, false
	for _, m := range o.chanTexts() {
		if strings.Contains(m, "25") {
			found = true
		}
		if strings.Contains(m, cards.Render(tb.players[0].hole[:])) {
			foundCards = true // the winner's hole cards ride the win line
		}
	}
	if !found {
		t.Errorf("no win line mentioning 25: %v", o.chanTexts())
	}
	if !foundCards {
		t.Errorf("win line must show the winner's cards: %v", o.chanTexts())
	}
}

func TestShowdownSplitOddChip(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	board := []cards.Card{card(7, cards.Spades), card(8, cards.Diamonds), card(9, cards.Clubs), card(11, cards.Hearts), card(13, cards.Spades)}
	riverState(tb, map[int][2]cards.Card{
		0: {card(5, cards.Clubs), card(5, cards.Diamonds)},  // pair 5
		1: {card(5, cards.Hearts), card(5, cards.Spades)},    // pair 5 — tie with P0
		2: {card(2, cards.Clubs), card(3, cards.Diamonds)},   // king high, loses
	}, []int{10, 10, 5}, board)

	o := newOut()
	endHand(tb, "ru", o)

	// Pots: level 5 → 15 chips for all three (winners P0+P1, odd chip to the
	// first winner left of the button = P1); level 10 → 10 chips for P0+P1.
	if got := tb.players[0].stack; got != 202 {
		t.Errorf("P0 stack = %d, want 202", got)
	}
	if got := tb.players[1].stack; got != 203 {
		t.Errorf("P1 stack = %d, want 203 (odd chip)", got)
	}
	if got := tb.players[2].stack; got != 195 {
		t.Errorf("P2 stack = %d, want 195", got)
	}
	joined := strings.Join(o.chanTexts(), "\n")
	if !strings.Contains(joined, "Вскрытие") || !strings.Contains(joined, "Делёжка") {
		t.Errorf("missing showdown/split lines: %v", o.chanTexts())
	}
	// Split winners' cards show in the split line; losers' stay hidden.
	if !strings.Contains(joined, "5♥️ 5♠️") || !strings.Contains(joined, "5♣️ 5♦️") {
		t.Errorf("split line must show both winners' cards: %v", o.chanTexts())
	}
	if strings.Contains(joined, "2♣️ 3♦️") {
		t.Errorf("loser's cards must stay hidden: %v", o.chanTexts())
	}
}

func TestSidePotAward(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	board := []cards.Card{card(2, cards.Spades), card(3, cards.Spades), card(4, cards.Diamonds), card(5, cards.Clubs), card(9, cards.Hearts)}
	riverState(tb, map[int][2]cards.Card{
		0: {card(14, cards.Hearts), card(14, cards.Diamonds)}, // pair aces
		1: {card(13, cards.Hearts), card(13, cards.Diamonds)}, // pair kings
		2: {card(6, cards.Hearts), card(7, cards.Hearts)},     // straight 3–7
	}, []int{50, 50, 20}, board)

	o := newOut()
	endHand(tb, "ru", o)

	// Main pot 60 (3×20): the straight wins. Side pot 60 (2×30): aces beat kings.
	if got := tb.players[2].stack; got != 240 {
		t.Errorf("P2 stack = %d, want 240 (main pot)", got)
	}
	if got := tb.players[0].stack; got != 210 {
		t.Errorf("P0 stack = %d, want 210 (side pot)", got)
	}
	if got := tb.players[1].stack; got != 150 {
		t.Errorf("P1 stack = %d, want 150", got)
	}
	// Each win line carries its winner's hole cards.
	joined := strings.Join(o.chanTexts(), "\n")
	if !strings.Contains(joined, "6♥️ 7♥️") || !strings.Contains(joined, "A♥️ A♦️") {
		t.Errorf("win lines must show winners' cards: %v", o.chanTexts())
	}
}

func TestAllinRunout(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	dealHand(tb, "ru", newOut())

	// Force both blinds all-in; P0 calls — no more betting possible, so the
	// board runs out to the river and the hand settles at showdown.
	tb.players[1].allin = true
	tb.players[2].allin = true
	tb.pending = map[int]bool{0: true}
	act(t, tb, 0, "call")

	if len(tb.board) != 5 {
		t.Fatalf("runout board = %d cards, want 5", len(tb.board))
	}
	if tb.phase != phasePause {
		t.Errorf("phase = %d, want pause after showdown", tb.phase)
	}
}

func TestTimeoutAutoActionsAndRemoval(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	dealHand(tb, "ru", newOut())

	// Facing the BB: timeout folds.
	o := newOut()
	tb.autoAct("ru", o)
	if !tb.players[0].folded || tb.players[0].timeouts != 1 {
		t.Fatalf("timeout must fold P0: folded=%v timeouts=%d", tb.players[0].folded, tb.players[0].timeouts)
	}
	if tb.toAct != 1 {
		t.Errorf("action moved to P%d, want P1", tb.toAct)
	}
	if !containsAll(strings.Join(o.chanTexts(), "\n"), "авто-фолд", "1/2") {
		t.Errorf("missing timeout notice: %v", o.chanTexts())
	}

	// Second timeout for P1 folds them too; P2 takes the hand, pause opens.
	tb.autoAct("ru", newOut()) // P1 folds, P2 wins
	if tb.phase != phasePause {
		t.Fatalf("phase=%d want pause", tb.phase)
	}
	tb.players[0].timeouts = 1 // P0 already struck once last hand
	onDealPause(tb)            // deal the next hand

	// Put P0 (one strike) on the clock facing a bet and let it time out:
	// second strike removes them. P0 is this hand's big blind, so raise the
	// bet level to force the fold branch.
	p0 := -1
	for i, s := range tb.players {
		if s.keyHex == keyHexFor(0) {
			p0 = i
		}
	}
	tb.toAct = p0
	tb.curBet += 50
	tb.pending = map[int]bool{p0: true}
	o = newOut()
	tb.autoAct("ru", o) // P0 auto-folds with timeouts=2 — strikeout

	// End the hand: the flop prompt went to P2 (in turn order after P0's
	// fold); P2's fold leaves P1 last live, closing the hand and running the
	// removal in the same output.
	of := act(t, tb, 2, "fold")

	if len(tb.players) != 2 || tb.players[0].name != "P1" {
		t.Fatalf("P0 not removed after 2 consecutive timeouts: %v", tb.players)
	}
	if !strings.Contains(strings.Join(of.chanTexts(), "\n"), "таймауты") {
		t.Errorf("missing removal notice: %v", of.chanTexts())
	}
}

func TestStaleAndInvalidActions(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	dealHand(tb, "ru", newOut())

	// Out of turn.
	o := act(t, tb, 1, "call")
	if !strings.Contains(o.replyMsgs[0], "Не твой ход") || !strings.Contains(o.replyMsgs[0], "P0") {
		t.Fatalf("out-of-turn reply = %v", o.replyMsgs)
	}
	// Check into a bet.
	o = act(t, tb, 0, "check")
	if !strings.Contains(o.replyMsgs[0], "10") {
		t.Fatalf("check-denied reply = %v", o.replyMsgs)
	}
	// Act twice.
	act(t, tb, 0, "fold")
	o = act(t, tb, 0, "call")
	if !strings.Contains(o.replyMsgs[0], "сбросил") {
		t.Fatalf("folded reply = %v", o.replyMsgs)
	}
	// A stranger DMs an action: no table, no reply noise.
	o = newOut()
	dmAction("ru", hex.EncodeToString([]byte("raw99")), "call", o)
	if !strings.Contains(o.replyMsgs[0], "не в раздаче") {
		t.Fatalf("stranger reply = %v", o.replyMsgs)
	}
}

func TestLeaveEndMatrix(t *testing.T) {
	t.Run("bystander end denied", func(t *testing.T) {
		setupTest(t)
		tb := mkTable(3)
		dealHand(tb, "ru", newOut())
		o := newOut()
		chEnd("ru", "chan", hex.EncodeToString([]byte("xx")), o)
		if !strings.Contains(o.replyMsgs[0], "открывший") || tables["chan"] != tb {
			t.Fatalf("bystander must not close: %v", o.replyMsgs)
		}
	})
	t.Run("seated non-opener end is leave", func(t *testing.T) {
		setupTest(t)
		tb := mkTable(3)
		dealHand(tb, "ru", newOut())
		o := newOut()
		chEnd("ru", "chan", keyHexFor(2), o)
		if !tb.players[2].leaving || len(tb.players) != 3 {
			t.Fatalf("non-opener end must equal leave: %v", o.replyMsgs)
		}
	})
	t.Run("opener end closes and refunds", func(t *testing.T) {
		setupTest(t)
		tb := mkTable(3)
		dealHand(tb, "ru", newOut())
		act(t, tb, 0, "raise") // P0 has 20 in the pot
		o := newOut()
		chEnd("ru", "chan", keyHexFor(0), o)
		if tables["chan"] != nil {
			t.Fatal("table must be closed")
		}
		for i, want := range []int{200, 200, 200} {
			if got := tb.players[i].stack; got != want {
				t.Errorf("P%d stack = %d, want refund to %d", i, got, want)
			}
		}
	})
	t.Run("opener gone: seated player may end", func(t *testing.T) {
		setupTest(t)
		tb := mkTable(3)
		dealHand(tb, "ru", newOut())
		tb.players = tb.players[1:] // the opener left; button drift is fine here
		o := newOut()
		chEnd("ru", "chan", keyHexFor(1), o)
		if tables["chan"] != nil {
			t.Fatal("table must close when the opener is gone")
		}
	})
}

func TestJoinQueueAndPauseSeating(t *testing.T) {
	setupTest(t)
	tb := mkTable(2)
	dealHand(tb, "ru", newOut())

	o := newOut()
	chJoin("ru", "chan", keyHexFor(2), rawKeyFor(2), "P2", o)
	if len(tb.waiting) != 1 || !strings.Contains(o.replyMsgs[0], "3/6") {
		t.Fatalf("mid-hand join must queue: waiting=%d reply=%v", len(tb.waiting), o.replyMsgs)
	}

	// End the hand by folds: postHand seats the queue and opens the pause.
	act(t, tb, 0, "raise")
	act(t, tb, 1, "fold")
	if len(tb.players) != 3 || tb.phase != phasePause { // P0, P1 + the queued P2
		t.Fatalf("after foldout: players=%d phase=%d", len(tb.players), tb.phase)
	}

	onDealPause(tb)
	if len(tb.players) != 3 {
		t.Fatalf("after pause deal: players=%d, want 3", len(tb.players))
	}
	for _, s := range tb.players {
		if !s.dealt {
			t.Fatalf("%s not dealt after pause", s.name)
		}
	}
}

func TestChStartGatesAndJoinWindow(t *testing.T) {
	setupTest(t)
	// Unknown contact (empty channel key): the pinned advert failure.
	o := newOut()
	chStart("ru", "chan", "", "", "X", o)
	if !strings.Contains(o.replyMsgs[0], "адверта") || tables["chan"] != nil {
		t.Fatalf("advert gate failed: %v", o.replyMsgs)
	}

	// Real opener: table opens, opener seated, join window armed.
	o = newOut()
	chStart("ru", "chan", keyHexFor(0), rawKeyFor(0), "P0", o)
	tb := tables["chan"]
	if tb == nil || len(tb.players) != 1 || tb.joinT == nil {
		t.Fatalf("start failed: %v %v", tb, o.replyMsgs)
	}
	if len(o.chanTexts()) == 0 || len(o.dmTexts(rawKeyFor(0))) == 0 {
		t.Errorf("missing table_open/welcome: chan=%v dms=%v", o.chanTexts(), o.dmTexts(rawKeyFor(0)))
	}

	// The join window fires short: polite close.
	onJoinWindow(tb)
	if tables["chan"] != nil {
		t.Fatal("short join window must close the table")
	}
}

func TestParseActionVerb(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"call", "call", true},
		{".call", "call", true},
		{".poker call", "call", true},
		{"poker raise 40", "raise", true},
		{".poker raise 40", "raise", true},
		{"call 999", "call", true},
		{"fold", "fold", true},
		{"check", "check", true},
		{"bet 20", "bet", true},
		{"allin", "allin", true},
		{"", "", false},
		{"cards", "", false},
		{".poker", "", false},
		{"hello", "", false},
	}
	for _, tc := range cases {
		got, ok := parseActionVerb(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseActionVerb(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMatchesAndRouting(t *testing.T) {
	setupTest(t)
	mkTable(2)
	p := plugin{}

	dmSeated := registry.NewCtx("call", "", rawKeyFor(0), "P0", nil, nil)
	if !p.Matches(dmSeated) {
		t.Error("DM action from a seated player must match")
	}
	dmStranger := registry.NewCtx("call", "", "raw99", "X", nil, nil)
	if p.Matches(dmStranger) {
		t.Error("DM action from a stranger must not match")
	}
	if !p.Matches(registry.NewCtx(".poker cards", "", rawKeyFor(0), "P0", nil, nil)) {
		t.Error("DM .poker prefix must match")
	}
	if !p.Matches(registry.NewCtx(".poker", "chan", "aa", "X", nil, nil)) {
		t.Error("channel .poker must match")
	}
	if p.Matches(registry.NewCtx("call", "chan", "aa", "X", nil, nil)) {
		t.Error("channel action verb must not match")
	}
}

func TestChannelActionGuidance(t *testing.T) {
	setupTest(t)
	tb := mkTable(3)
	dealHand(tb, "ru", newOut())
	before := tb.toAct

	ctx := registry.NewCtx(".poker call", "chan", keyHexFor(0), "P0", nil, nil)
	reply, err := p_handle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply[0], "DM only") || tb.toAct != before {
		t.Fatalf("guidance must not change state: %q toAct=%d", reply[0], tb.toAct)
	}
}

// p_handle runs the plugin Handle for routing-level tests.
func p_handle(c registry.Ctx) ([]string, error) { return plugin{}.Handle(c) }

func TestPromptPricing(t *testing.T) {
	setupTest(t)

	t.Run("facing bet", func(t *testing.T) {
		tb := mkTable(3)
		dealHand(tb, "ru", newOut()) // UTG faces the BB
		got := buildActions(tb, tb.toAct, "ru")
		if !strings.Contains(got, "call 10") || !strings.Contains(got, "raise до 20") || strings.Contains(got, "check") {
			t.Errorf("actions = %q", got)
		}
	})
	t.Run("free check", func(t *testing.T) {
		tb := mkTable(3)
		dealHand(tb, "ru", newOut())
		tb.toAct = 2 // BB option after calls around
		got := buildActions(tb, 2, "ru")
		if !strings.Contains(got, "check") || !strings.Contains(got, "raise до 20") {
			t.Errorf("actions = %q", got)
		}
	})
	t.Run("raise cap", func(t *testing.T) {
		tb := mkTable(3)
		dealHand(tb, "ru", newOut())
		tb.raiseCnt = maxRaises()
		if got := buildActions(tb, 0, "ru"); strings.Contains(got, "raise") {
			t.Errorf("capped actions = %q", got)
		}
	})
	t.Run("short stack all-in price", func(t *testing.T) {
		tb := mkTable(3)
		dealHand(tb, "ru", newOut())
		tb.players[0].stack = 7
		got := buildActions(tb, 0, "ru")
		if !strings.Contains(got, "call 7 (все)") || strings.Contains(got, "raise") {
			t.Errorf("short actions = %q", got)
		}
	})
}

func TestTurnPromptByteBudget(t *testing.T) {
	setupTest(t)
	for _, lang := range []string{"ru", "en"} {
		tb := mkTable(2)
		tb.players[0].stack = 1000 // deep stacks so the prices render literally
		tb.players[1].stack = 1000
		riverState(tb, map[int][2]cards.Card{
			0: {card(10, cards.Spades), card(10, cards.Hearts)},
			1: {card(2, cards.Diamonds), card(3, cards.Clubs)},
		}, []int{620, 620},
			[]cards.Card{card(14, cards.Spades), card(13, cards.Hearts), card(12, cards.Diamonds), card(11, cards.Clubs), card(9, cards.Spades)})
		tb.curBet = 40 // big-bet street prices
		o := newOut()
		o.turnDM(tb, 0, lang)
		msg := o.dmTexts(rawKeyFor(0))[0]
		if len(msg) > wireBudget {
			t.Errorf("%s turn prompt %d bytes (max %d): %q", lang, len(msg), wireBudget, msg)
		}
		if !strings.Contains(msg, "call 40") {
			t.Errorf("%s prompt missing priced call: %q", lang, msg)
		}
	}
}

func TestDMStatusCardsLog(t *testing.T) {
	setupTest(t)
	tb := mkTable(2)
	dealHand(tb, "ru", newOut())

	status := dmStatus(tb, tb.players[0], "ru")
	if !strings.Contains(status, "банк") || !strings.Contains(status, "Префлоп") {
		t.Errorf("dm status = %q", status)
	}
	cardsMsg := cardsReply(tb, tb.players[0], "ru")
	if cardsMsg != cards.Render(tb.players[0].hole[:]) {
		t.Errorf("cards reply = %q", cardsMsg)
	}
	tb.players[0].folded = true
	if got := dmStatus(tb, tb.players[0], "ru"); !strings.Contains(got, "не в этой раздаче") {
		t.Errorf("folded status = %q", got)
	}

	log := logReply(tb.players[1], "ru")
	if len(log) < 2 || !strings.Contains(log[0], "события") {
		t.Errorf("log = %v", log)
	}
	if n := len(tb.players[1].log); n > logCap {
		t.Errorf("ring buffer holds %d entries, cap %d", n, logCap)
	}
}

func TestHandlePlaysFullHandHeadsUp(t *testing.T) {
	setupTest(t)
	dm := func(text string, i int) []string {
		t.Helper()
		reply, err := p_handle(registry.NewCtx(text, "", rawKeyFor(i), fmt.Sprintf("P%d", i), nil, nil))
		if err != nil {
			t.Fatalf("handle %q: %v", text, err)
		}
		return reply
	}
	chanReply := func(text string, i int) []string {
		t.Helper()
		reply, err := p_handle(registry.NewCtx(text, "chan", keyHexFor(i), fmt.Sprintf("P%d", i), nil, nil))
		if err != nil {
			t.Fatalf("handle %q: %v", text, err)
		}
		return reply
	}

	chanReply(".poker start", 0)
	chanReply(".poker join", 1)
	tb := tables["chan"]
	if tb == nil || len(tb.players) != 2 {
		t.Fatalf("table not filled: %v", tb)
	}
	onJoinWindow(tb) // deal
	if tb.phase != phaseHand {
		t.Fatalf("phase=%d, want hand", tb.phase)
	}

	// Heads-up: P0 (button/SB) calls 5 more, P1 (BB) checks; then both check
	// every street to the showdown. Postflop the non-button (P1) acts first.
	// The ✅ confirm now travels as an announce before the dealer line
	// (verified in TestDeliveryOrder), so the Handle reply carries nothing.
	dm("call", 0)
	dm(".poker check", 1) // dotted form through Handle
	for street := 0; street < 3; street++ {
		dm("check", 1)
		dm("check", 0)
	}

	if tb.phase != phasePause || len(tb.board) != 5 {
		t.Fatalf("after hand: phase=%d board=%d", tb.phase, len(tb.board))
	}
	sum := 0
	for _, s := range tb.players {
		sum += s.stack
	}
	if sum != 400 {
		t.Errorf("chips leaked: stacks sum %d, want 400", sum)
	}
}

// TestDeliveryOrder pins the announce order over the serialized radio sender:
// deal DMs in seat order after the channel line, and — for a player action —
// the actor's ✅ confirm FIRST, then the channel dealer line, then the next
// player's turn prompt. A previous map-based collector randomized the DM
// order and replies enqueued after flush put the confirm last.
func TestDeliveryOrder(t *testing.T) {
	setupTest(t)
	var seq []string
	games.SetAnnouncer(func(scope string, msgs []string) {
		for _, m := range msgs {
			seq = append(seq, scope+"|"+m)
		}
	})
	t.Cleanup(func() { games.SetAnnouncer(func(string, []string) {}) })

	tb := mkTable(2)
	o := newOut()
	dealHand(tb, "ru", o)
	o.flush()
	if len(seq) != 4 {
		t.Fatalf("deal produced %d messages, want 4 (deal line + 2 hole cards + turn): %v", len(seq), seq)
	}
	if !strings.HasPrefix(seq[0], "chan|🎴") ||
		!strings.HasPrefix(seq[1], "dm:"+rawKeyFor(0)+"|🎴") ||
		!strings.HasPrefix(seq[2], "dm:"+rawKeyFor(1)+"|🎴") ||
		!strings.HasPrefix(seq[3], "dm:"+rawKeyFor(0)+"|Твой ход") {
		t.Fatalf("deal order wrong:\n%s", strings.Join(seq, "\n"))
	}

	seq = nil
	o = act(t, tb, 0, "call")
	o.flush()
	if len(o.replyMsgs) != 0 {
		t.Errorf("successful action must confirm via announce, got reply %v", o.replyMsgs)
	}
	if len(seq) != 3 {
		t.Fatalf("call produced %d messages, want 3 (confirm + dealer line + next turn): %v", len(seq), seq)
	}
	if !strings.HasPrefix(seq[0], "dm:"+rawKeyFor(0)+"|✅") {
		t.Errorf("actor confirm must be first, got %q", seq[0])
	}
	if !strings.HasPrefix(seq[1], "chan|") {
		t.Errorf("dealer line must follow the confirm, got %q", seq[1])
	}
	if !strings.HasPrefix(seq[2], "dm:"+rawKeyFor(1)+"|Твой ход") {
		t.Errorf("next turn prompt must be last, got %q", seq[2])
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
