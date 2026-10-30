package cards

import (
	"bytes"
	"testing"
)

// c builds a card from a raw rank (2–14) and suit, keeping test vectors short.
func c(r uint8, s Suit) Card { return New(Rank(r), s) }

func hand(cs ...Card) [5]Card {
	var h [5]Card
	copy(h[:], cs)
	return h
}

func TestScore5Categories(t *testing.T) {
	tests := []struct {
		name string
		cards [5]Card
		wantCat uint8
		wantRanks [5]uint8
	}{
		{"high card", hand(c(14, Spades), c(13, Hearts), c(9, Diamonds), c(7, Clubs), c(2, Spades)), HighCard, [5]uint8{14, 13, 9, 7, 2}},
		{"pair", hand(c(14, Spades), c(14, Hearts), c(13, Diamonds), c(9, Clubs), c(2, Spades)), Pair, [5]uint8{14, 13, 9, 2}},
		{"two pair", hand(c(14, Spades), c(14, Hearts), c(13, Diamonds), c(13, Clubs), c(9, Spades)), TwoPair, [5]uint8{14, 13, 9}},
		{"trips", hand(c(14, Spades), c(14, Hearts), c(14, Diamonds), c(13, Clubs), c(9, Spades)), Trips, [5]uint8{14, 13, 9}},
		{"straight", hand(c(5, Spades), c(6, Hearts), c(7, Diamonds), c(8, Clubs), c(9, Spades)), Straight, [5]uint8{9}},
		{"wheel", hand(c(14, Spades), c(2, Hearts), c(3, Diamonds), c(4, Clubs), c(5, Spades)), Straight, [5]uint8{5}},
		{"flush", hand(c(14, Hearts), c(13, Hearts), c(9, Hearts), c(7, Hearts), c(2, Hearts)), Flush, [5]uint8{14, 13, 9, 7, 2}},
		{"full house", hand(c(14, Spades), c(14, Hearts), c(14, Diamonds), c(13, Clubs), c(13, Spades)), FullHouse, [5]uint8{14, 13}},
		{"quads", hand(c(14, Spades), c(14, Hearts), c(14, Diamonds), c(14, Clubs), c(9, Spades)), Quads, [5]uint8{14, 9}},
		{"straight flush", hand(c(5, Spades), c(6, Spades), c(7, Spades), c(8, Spades), c(9, Spades)), StraightFlush, [5]uint8{9}},
		{"royal", hand(c(10, Hearts), c(11, Hearts), c(12, Hearts), c(13, Hearts), c(14, Hearts)), StraightFlush, [5]uint8{14}},
		{"trips vs full house not confused", hand(c(7, Spades), c(7, Hearts), c(7, Diamonds), c(7, Clubs), c(2, Spades)), Quads, [5]uint8{7, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Score5(tt.cards)
			if got.Cat != tt.wantCat || got.Ranks != tt.wantRanks {
				t.Fatalf("Score5 = Cat %d Ranks %v, want Cat %d Ranks %v", got.Cat, got.Ranks, tt.wantCat, tt.wantRanks)
			}
		})
	}
}

func TestCompareKickersAndTies(t *testing.T) {
	pairAK := Score5(hand(c(14, Spades), c(14, Hearts), c(13, Diamonds), c(9, Clubs), c(2, Spades)))
	pairAQ := Score5(hand(c(14, Clubs), c(14, Diamonds), c(12, Spades), c(9, Hearts), c(2, Clubs)))
	if Compare(pairAK, pairAQ) <= 0 {
		t.Error("pair A/K must beat pair A/Q")
	}
	wheel := Score5(hand(c(14, Spades), c(2, Hearts), c(3, Diamonds), c(4, Clubs), c(5, Spades)))
	sixHigh := Score5(hand(c(2, Hearts), c(3, Diamonds), c(4, Clubs), c(5, Hearts), c(6, Spades)))
	if Compare(wheel, sixHigh) >= 0 {
		t.Error("6-high straight must beat the wheel")
	}
	twin := Score5(hand(c(14, Diamonds), c(14, Clubs), c(13, Hearts), c(9, Spades), c(2, Hearts)))
	if Compare(pairAK, twin) != 0 {
		t.Error("identical ranks in different suits must tie (split pot)")
	}
	if Compare(pairAK, Score5(hand(c(14, Spades), c(13, Hearts), c(11, Diamonds), c(9, Clubs), c(7, Spades)))) <= 0 {
		t.Error("pair must beat high card")
	}
}

func TestBest7FindsHiddenHand(t *testing.T) {
	// Hole A♥️ K♦️ on a four-heart board: the best 5 of 7 is the flush, not the
	// pair of aces the hole cards suggest.
	var seven [7]Card
	copy(seven[:], []Card{
		c(14, Hearts), c(13, Diamonds),
		c(12, Hearts), c(11, Hearts), c(7, Hearts), c(3, Hearts), c(2, Clubs),
	})
	sc, best := Best7(seven)
	if sc.Cat != Flush {
		t.Fatalf("category = %d, want Flush", sc.Cat)
	}
	if best[0].Rank() != 14 {
		t.Errorf("flush top card = %v, want ace", best[0].Rank())
	}

	// Royal flush assembled from board + one hole card.
	copy(seven[:], []Card{
		c(14, Spades), c(6, Diamonds),
		c(10, Spades), c(11, Spades), c(12, Spades), c(13, Spades), c(2, Hearts),
	})
	if sc, _ = Best7(seven); sc.Cat != StraightFlush || sc.Ranks[0] != 14 {
		t.Fatalf("got Cat %d high %d, want royal straight flush", sc.Cat, sc.Ranks[0])
	}
}

func TestRefundUncalled(t *testing.T) {
	if r := RefundUncalled([]int{80, 50}); r[0] != 30 || r[1] != 0 {
		t.Errorf("uncalled 80 vs 50: refund %v, want [30 0]", r)
	}
	if r := RefundUncalled([]int{50, 50, 20}); r[0] != 0 || r[1] != 0 || r[2] != 0 {
		t.Errorf("called-around pot must refund nothing, got %v", r)
	}
	if r := RefundUncalled([]int{50, 80, 80}); r[0] != 0 || r[1] != 0 || r[2] != 0 {
		t.Errorf("tie at the top: no refund, got %v", r)
	}
}

func TestSplitPots(t *testing.T) {
	t.Run("flat", func(t *testing.T) {
		pots := SplitPots([]int{10, 10, 10}, []bool{true, true, true})
		if len(pots) != 1 || pots[0].Amount != 30 || len(pots[0].Eligible) != 3 {
			t.Fatalf("got %+v, want one 30-chip pot for all", pots)
		}
	})
	t.Run("side pot", func(t *testing.T) {
		// Player 2 all-in for 20; 0 and 1 contest the side pot. Total 120 =
		// main 3×20 + side 2×30.
		pots := SplitPots([]int{50, 50, 20}, []bool{true, true, true})
		if len(pots) != 2 {
			t.Fatalf("got %d pots, want 2: %+v", len(pots), pots)
		}
		if pots[0].Amount != 60 || len(pots[0].Eligible) != 3 {
			t.Errorf("main pot = %+v, want 60 for all three", pots[0])
		}
		if pots[1].Amount != 60 || len(pots[1].Eligible) != 2 {
			t.Errorf("side pot = %+v, want 60 for players 0,1", pots[1])
		}
	})
	t.Run("folded contributions merge", func(t *testing.T) {
		// Player 2 folded after contributing 20; their chips join the pot the
		// live players contest, and identical eligible sets merge into one pot.
		pots := SplitPots([]int{50, 50, 20}, []bool{true, true, false})
		if len(pots) != 1 || pots[0].Amount != 120 || len(pots[0].Eligible) != 2 {
			t.Fatalf("got %+v, want one 120-chip pot for players 0,1", pots)
		}
	})
	t.Run("dead layer rolls up", func(t *testing.T) {
		// Pathological order: a folded player holds the top contribution level;
		// their excess folds into the deepest winnable pot instead of vanishing.
		pots := SplitPots([]int{80, 50, 50}, []bool{false, true, true})
		if len(pots) != 1 || pots[0].Amount != 180 {
			t.Fatalf("got %+v, want one 180-chip pot for players 1,2", pots)
		}
	})
}

func TestShuffleSanity(t *testing.T) {
	for i := 0; i < 50; i++ {
		d := Deck()
		Shuffle(d)
		seen := map[Card]bool{}
		for _, card := range d {
			if card > New(14, Clubs) || seen[card] {
				t.Fatalf("shuffle %d: bad or duplicate card %v", i, card)
			}
			seen[card] = true
		}
		if len(seen) != 52 {
			t.Fatalf("shuffle %d: %d distinct cards, want 52", i, len(seen))
		}
	}
	// Distribution smoke: after many shuffles, the top card should vary.
	d := Deck()
	Shuffle(d)
	first := d[0]
	varies := false
	for i := 0; i < 20 && !varies; i++ {
		d := Deck()
		Shuffle(d)
		if d[0] != first {
			varies = true
		}
	}
	if !varies {
		t.Error("top card never changed across 20 shuffles")
	}
}

func TestRenderBytes(t *testing.T) {
	for r := Rank(2); r <= 14; r++ {
		for s := Spades; s <= Clubs; s++ {
			card := New(r, s)
			if n := len(card.String()); n > 8 {
				t.Errorf("card %q renders in %d bytes, budget is 8", card, n)
			}
		}
	}
	for _, s := range suitStr {
		if len(s) != 6 { // base char (3 bytes) + U+FE0F variation selector
			t.Errorf("suit %q is %d bytes, want exactly 6 (FE0F required)", s, len(s))
		}
	}
	board := []Card{c(10, Spades), c(11, Hearts), c(12, Diamonds), c(13, Clubs), c(14, Spades)}
	if n := len(Render(board)); n > 44 { // 5 cards × 8 bytes + 4 separators
		t.Errorf("full board renders in %d bytes, budget is 44", n)
	}
	if !bytes.Contains([]byte(Render(board)), []byte("10♠️")) {
		t.Error("ten of spades must render as 10♠️")
	}
}
