// Package cards is the pure card engine for the .poker game: card representation
// with the pinned emoji rendering (♠️♥️♦️♣️, base char + U+FE0F), deck building
// and crypto-random shuffling, 5/7-card hand evaluation, and side-pot splitting.
// It depends only on the standard library so it stays reusable and testable.
package cards

import (
	"crypto/rand"
	"fmt"
	"math/big"
	mrand "math/rand"
	"sort"
	"strings"
)

// Suit enumerates the four suits in the pinned display order ♠️♥️♦️♣️.
type Suit uint8

const (
	Spades Suit = iota
	Hearts
	Diamonds
	Clubs
)

var suitStr = [4]string{"♠️", "♥️", "♦️", "♣️"}

// Rank is a card rank, 2 through 14 (ace).
type Rank uint8

// String returns the rank symbol: 2–9, 10, J, Q, K, A.
func (r Rank) String() string {
	switch r {
	case 14:
		return "A"
	case 13:
		return "K"
	case 12:
		return "Q"
	case 11:
		return "J"
	case 10:
		return "10"
	default:
		return fmt.Sprintf("%d", uint8(r))
	}
}

// Card is a packed playing card: rank*4 + suit.
type Card uint8

// New returns the Card for rank r and suit s.
func New(r Rank, s Suit) Card { return Card(r)*4 + Card(s) }

// Rank unpacks the card's rank.
func (c Card) Rank() Rank { return Rank(c / 4) }

// Suit unpacks the card's suit.
func (c Card) Suit() Suit { return Suit(c % 4) }

// String renders the card in the pinned notation, e.g. "A♠️" or "10♦️".
func (c Card) String() string { return c.Rank().String() + suitStr[c.Suit()] }

// Render joins cards with single spaces: "A♠️ K♥️ 2♦️".
func Render(cs []Card) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.String()
	}
	return strings.Join(parts, " ")
}

// Deck returns a fresh 52-card deck in rank-major order.
func Deck() []Card {
	d := make([]Card, 0, 52)
	for r := Rank(2); r <= 14; r++ {
		for s := Spades; s <= Clubs; s++ {
			d = append(d, New(r, s))
		}
	}
	return d
}

// randInt returns a uniform value in [0,n). It draws from crypto/rand so the
// bot deals fairly as the trusted dealer; the math/rand fallback only triggers
// on an OS entropy failure (essentially impossible) so dealing can never stall.
func randInt(n int) int {
	if v, err := rand.Int(rand.Reader, big.NewInt(int64(n))); err == nil {
		return int(v.Int64())
	}
	return mrand.Intn(n)
}

// Shuffle applies an in-place Fisher–Yates shuffle.
func Shuffle(deck []Card) {
	for i := len(deck) - 1; i > 0; i-- {
		j := randInt(i + 1)
		deck[i], deck[j] = deck[j], deck[i]
	}
}

// Hand categories, ordered weakest to strongest; the numeric order is the
// comparison order.
const (
	HighCard uint8 = iota
	Pair
	TwoPair
	Trips
	Straight
	Flush
	FullHouse
	Quads
	StraightFlush
)

// Score is a totally-ordered hand value: Cat first, then Ranks compared
// lexicographically (unused tail slots stay zero, so short tuples compare
// correctly against padded ones).
type Score struct {
	Cat   uint8
	Ranks [5]uint8
}

func tuple(vals ...uint8) [5]uint8 {
	var t [5]uint8
	copy(t[:], vals)
	return t
}

// straightHigh reports whether the distinct descending ranks form a straight
// and returns its high card (the wheel A-2-3-4-5 scores as a 5-high straight).
func straightHigh(ranks []uint8) (bool, uint8) {
	if len(ranks) != 5 {
		return false, 0
	}
	if ranks[0]-ranks[4] == 4 {
		return true, ranks[0]
	}
	if ranks[0] == 14 && ranks[1] == 5 && ranks[2] == 4 && ranks[3] == 3 && ranks[4] == 2 {
		return true, 5
	}
	return false, 0
}

// Score5 scores exactly five cards.
func Score5(hand [5]Card) Score {
	var cnt [15]uint8
	flush := true
	for i, c := range hand {
		cnt[c.Rank()]++
		if i > 0 && c.Suit() != hand[0].Suit() {
			flush = false
		}
	}
	var ranks []uint8 // distinct ranks, descending
	for r := uint8(14); r >= 2; r-- {
		if cnt[r] > 0 {
			ranks = append(ranks, r)
		}
	}

	var quad, trip, p1, p2 uint8 // quad/trips rank; two pair ranks hi>lo
	for r := uint8(14); r >= 2; r-- {
		switch cnt[r] {
		case 4:
			quad = r
		case 3:
			trip = r
		case 2:
			if p1 == 0 {
				p1 = r
			} else {
				p2 = r
			}
		}
	}
	kickers := func(exclude ...uint8) []uint8 {
		var ks []uint8
		for _, r := range ranks {
			skip := false
			for _, e := range exclude {
				if r == e {
					skip = true
					break
				}
			}
			if !skip {
				ks = append(ks, r)
			}
		}
		return ks
	}

	straight, sHigh := straightHigh(ranks)
	switch {
	case straight && flush:
		return Score{StraightFlush, tuple(sHigh)}
	case quad > 0:
		k := kickers(quad)
		return Score{Quads, tuple(quad, k[0])}
	case trip > 0 && p1 > 0:
		return Score{FullHouse, tuple(trip, p1)}
	case flush:
		return Score{Flush, tuple(ranks...)}
	case straight:
		return Score{Straight, tuple(sHigh)}
	case trip > 0:
		k := kickers(trip)
		return Score{Trips, tuple(trip, k[0], k[1])}
	case p1 > 0 && p2 > 0:
		k := kickers(p1, p2)
		return Score{TwoPair, tuple(p1, p2, k[0])}
	case p1 > 0:
		k := kickers(p1)
		return Score{Pair, tuple(p1, k[0], k[1], k[2])}
	default:
		return Score{HighCard, tuple(ranks...)}
	}
}

// Compare orders two scores: positive when a wins, negative when b wins, 0 on
// an exact tie (split pot).
func Compare(a, b Score) int {
	if a.Cat != b.Cat {
		if a.Cat > b.Cat {
			return 1
		}
		return -1
	}
	for i := 0; i < 5; i++ {
		if a.Ranks[i] != b.Ranks[i] {
			if a.Ranks[i] > b.Ranks[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

// Best7 evaluates all 21 five-card combinations of a 7-card hand (two hole +
// five board) and returns the best score with its five cards.
func Best7(cs [7]Card) (Score, [5]Card) {
	var best Score
	var bestHand [5]Card
	for i := 0; i < 7; i++ {
		for j := i + 1; j < 7; j++ {
			var h [5]Card
			n := 0
			for k, c := range cs {
				if k == i || k == j {
					continue
				}
				h[n] = c
				n++
			}
			if s := Score5(h); Compare(s, best) > 0 {
				best, bestHand = s, h
			}
		}
	}
	return best, bestHand
}

// Pot is one side-pot layer: an amount and the player indexes eligible to win
// it (indexes into the contribution slice passed to SplitPots).
type Pot struct {
	Amount   int
	Eligible []int
}

// RefundUncalled returns the portion of each contribution that no one matched —
// when exactly one player holds the highest contribution level, everything
// above the second-highest level goes back to them (the classic uncalled-bet
// refund). The caller adds the refund back to the player's stack.
func RefundUncalled(contribs []int) []int {
	refund := make([]int, len(contribs))
	max1, max2, atMax := 0, 0, -1
	for i, c := range contribs {
		switch {
		case c > max1:
			max2, max1, atMax = max1, c, i
		case c == max1:
			max2 = c // tie at the top: everything was called
		case c > max2:
			max2 = c
		}
	}
	if atMax >= 0 && max1 > max2 {
		refund[atMax] = max1 - max2
	}
	return refund
}

// SplitPots layers contributions into pots at each distinct all-in level.
// Levels with identical eligible sets merge; dead money (a layer nobody live
// can win — folded chips) rolls into the first winnable pot.
func SplitPots(contribs []int, live []bool) []Pot {
	var levels []int
	seen := map[int]bool{}
	for _, c := range contribs {
		if c > 0 && !seen[c] {
			seen[c] = true
			levels = append(levels, c)
		}
	}
	sort.Ints(levels)

	var pots []Pot
	prev, dead := 0, 0
	for _, L := range levels {
		amount := 0
		var elig []int
		for i, c := range contribs {
			if c >= L {
				amount += L - prev
				if live[i] {
					elig = append(elig, i)
				}
			}
		}
		switch {
		case len(elig) == 0:
			dead += amount
		case len(pots) > 0 && sameElig(pots[len(pots)-1].Eligible, elig):
			pots[len(pots)-1].Amount += amount + dead
			dead = 0
		default:
			pots = append(pots, Pot{amount + dead, elig})
			dead = 0
		}
		prev = L
	}
	if dead > 0 && len(pots) > 0 { // no deeper level ever appeared: fold money to the last pot
		pots[len(pots)-1].Amount += dead
	}
	return pots
}

func sameElig(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
