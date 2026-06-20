// Package dice answers .dice: an immediate attributed roll, or a per-channel
// roll-off round (start/roll/end/status).
package dice

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"meshbot/internal/registry"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "dice",
		Triggers:  []string{".dice"},
		Match:     registry.Prefix,
		HelpShort: "roll dice",
		HelpLong:  ".dice [NdM] — кинуть кости (def 1d6)\n.dice start [NdM] / roll / end — раунд в канале",
		Group:     "Games",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processDice(c.Text, c.SenderName, c.Channel)
}

const (
	diceMaxCount   = 10   // max number of dice in one roll
	diceMinSides   = 2    // a die needs at least two faces
	diceMaxSides   = 1000 // upper bound on faces
	diceDefaultN   = 1    // default dice count when none given
	diceDefSides   = 6    // default faces (a d6)
	diceWinnersMax = 4    // ranked players listed in the result (mesh size)
)

// diceSession is an open roll-off round in a single channel. All players roll
// the same NdM; rolls are collected into a map keyed by player name so delivery
// order does not matter and a re-delivered message cannot change the outcome.
// A round stays open until someone runs ".dice end".
type diceSession struct {
	notation string         // human form, e.g. "2d6"
	n, sides int            // parsed dice count and faces
	rolls    map[string]int // playerName -> total
}

var (
	diceSessions = map[string]*diceSession{} // keyed by channel name
	diceMu       sync.Mutex
)

// processDice handles ".dice" and its roll-off sub-verbs.
//
//	.dice / .dice NdM / .dice M  – immediate attributed roll (DM or channel)
//	.dice start [NdM]            – open a round (channel only)
//	.dice roll                   – roll into the open round (channel only)
//	.dice end                    – close the round and announce the winner
//	.dice status                 – who has rolled so far
//
// senderName attributes rolls; channelName keys the roll-off (empty in DMs).
func processDice(text, senderName, channelName string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, ".dice"))
	fields := strings.Fields(rest)

	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}

	switch verb {
	case "start":
		return diceStart(strings.Join(fields[1:], " "), channelName)
	case "roll":
		return diceRoll(senderName, channelName)
	case "end":
		return diceEnd(channelName)
	case "status":
		return diceStatus(channelName)
	default:
		// Plain roll: ".dice", ".dice 2d6", ".dice 20".
		return dicePlainRoll(rest, senderName)
	}
}

// parseDiceSpec parses "NdM", "dM", "M", or "" into a dice count and face count,
// applying the configured caps. An empty spec yields the default 1d6.
func parseDiceSpec(spec string) (n, sides int, err error) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return diceDefaultN, diceDefSides, nil
	}

	if strings.Contains(spec, "d") {
		parts := strings.SplitN(spec, "d", 2)
		nStr := strings.TrimSpace(parts[0])
		sideStr := strings.TrimSpace(parts[1])

		n = diceDefaultN
		if nStr != "" {
			n, err = strconv.Atoi(nStr)
			if err != nil {
				return 0, 0, fmt.Errorf("bad dice count")
			}
		}
		sides, err = strconv.Atoi(sideStr)
		if err != nil {
			return 0, 0, fmt.Errorf("bad side count")
		}
	} else {
		// Bare number is a single die with that many faces.
		sides, err = strconv.Atoi(spec)
		if err != nil {
			return 0, 0, fmt.Errorf("bad number")
		}
		n = diceDefaultN
	}

	if n < 1 || n > diceMaxCount {
		return 0, 0, fmt.Errorf("count out of range")
	}
	if sides < diceMinSides || sides > diceMaxSides {
		return 0, 0, fmt.Errorf("sides out of range")
	}
	return n, sides, nil
}

// rollDice returns the individual results and their sum.
func rollDice(n, sides int) (results []int, total int) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	results = make([]int, n)
	for i := range results {
		v := r.Intn(sides) + 1
		results[i] = v
		total += v
	}
	return results, total
}

const diceUsage = "🎲 .dice [NdM] | .dice start [NdM] / roll / end"

// dicePlainRoll rolls immediately and attributes the result to the sender.
func dicePlainRoll(spec, senderName string) ([]string, error) {
	n, sides, err := parseDiceSpec(spec)
	if err != nil {
		return []string{diceUsage}, nil
	}
	results, total := rollDice(n, sides)

	// The send layer prefixes channel replies with "@[SenderName]", so the
	// roller's own name is not repeated here.
	if n == 1 {
		return []string{fmt.Sprintf("🎲 : %d", total)}, nil
	}
	parts := make([]string, len(results))
	for i, v := range results {
		parts[i] = strconv.Itoa(v)
	}
	return []string{fmt.Sprintf("🎲 : %s = %d", strings.Join(parts, "+"), total)}, nil
}

// diceStart opens a roll-off round in the channel.
func diceStart(spec, channelName string) ([]string, error) {
	if strings.TrimSpace(channelName) == "" {
		return []string{"Раунд доступен только в каналах"}, nil
	}
	n, sides, err := parseDiceSpec(spec)
	if err != nil {
		return []string{diceUsage}, nil
	}

	diceMu.Lock()
	defer diceMu.Unlock()

	if s := diceSessions[channelName]; s != nil {
		return []string{fmt.Sprintf("Раунд %s уже идёт. .dice roll", s.notation)}, nil
	}

	notation := fmt.Sprintf("%dd%d", n, sides)
	diceSessions[channelName] = &diceSession{
		notation: notation,
		n:        n,
		sides:    sides,
		rolls:    map[string]int{},
	}
	return []string{fmt.Sprintf("🎲 Раунд открыт: %s. Бросай: .dice roll", notation)}, nil
}

// diceRoll records the sender's roll in the open round (once per player).
func diceRoll(senderName, channelName string) ([]string, error) {
	if strings.TrimSpace(channelName) == "" {
		return []string{"Раунд доступен только в каналах"}, nil
	}

	diceMu.Lock()
	defer diceMu.Unlock()

	s := diceSessions[channelName]
	if s == nil {
		return []string{"Раунд не открыт. .dice start"}, nil
	}

	who := diceWho(senderName)
	if prev, ok := s.rolls[who]; ok {
		return []string{fmt.Sprintf("Ты уже бросил: %d", prev)}, nil
	}

	_, total := rollDice(s.n, s.sides)
	s.rolls[who] = total
	// Name omitted: the send layer prepends "@[SenderName]" on channel replies.
	return []string{fmt.Sprintf("🎲 : %d", total)}, nil
}

// diceEnd closes the round and announces the winner. Anyone may end it.
func diceEnd(channelName string) ([]string, error) {
	if strings.TrimSpace(channelName) == "" {
		return []string{"Раунд доступен только в каналах"}, nil
	}

	diceMu.Lock()
	defer diceMu.Unlock()

	s := diceSessions[channelName]
	if s == nil {
		return []string{"Раунд не открыт. .dice start"}, nil
	}
	delete(diceSessions, channelName)

	if len(s.rolls) == 0 {
		return []string{"Раунд закрыт, никто не бросил"}, nil
	}
	return []string{diceResult(s)}, nil
}

// diceStatus reports who has rolled in the open round.
func diceStatus(channelName string) ([]string, error) {
	if strings.TrimSpace(channelName) == "" {
		return []string{"Раунд доступен только в каналах"}, nil
	}

	diceMu.Lock()
	defer diceMu.Unlock()

	s := diceSessions[channelName]
	if s == nil {
		return []string{"Раунд не открыт. .dice start"}, nil
	}
	if len(s.rolls) == 0 {
		return []string{fmt.Sprintf("Раунд %s: бросков пока нет", s.notation)}, nil
	}
	return []string{diceResult(s)}, nil
}

// diceResult renders the ranked standings, handling ties at the top.
func diceResult(s *diceSession) string {
	type entry struct {
		name  string
		total int
	}
	entries := make([]entry, 0, len(s.rolls))
	for name, total := range s.rolls {
		entries = append(entries, entry{name, total})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].total != entries[j].total {
			return entries[i].total > entries[j].total
		}
		return entries[i].name < entries[j].name
	})

	best := entries[0].total
	var winners []string
	for _, e := range entries {
		if e.total == best {
			winners = append(winners, e.name)
		}
	}

	var head string
	if len(winners) > 1 {
		head = fmt.Sprintf("🤝 Ничья: %s — %d", strings.Join(winners, ", "), best)
	} else {
		head = fmt.Sprintf("🏆 %s: %d", entries[0].name, best)
	}

	// Append a compact tail of the next places, capped for mesh size.
	var others []string
	for _, e := range entries[len(winners):] {
		if len(others) >= diceWinnersMax {
			break
		}
		others = append(others, fmt.Sprintf("%s %d", e.name, e.total))
	}
	if len(others) > 0 {
		head += " (" + strings.Join(others, ", ") + ")"
	}
	return head
}

// diceWho returns a usable player label, falling back when the name is empty.
func diceWho(senderName string) string {
	if name := strings.TrimSpace(senderName); name != "" {
		return name
	}
	return "?"
}
