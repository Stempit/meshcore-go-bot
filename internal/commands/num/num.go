// Package num answers .num: guess-the-number, shared in channels and private in
// DMs, with a 30-day win leaderboard.
package num

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"meshbot/internal/commands/registry"
	"meshbot/internal/games"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "num",
		Triggers:  []string{".num"},
		Match:     registry.Prefix,
		HelpShort: "guess the number",
		HelpLong:  ".num <число> — угадай число (1–100)\n.num start [max] / status / end / top",
		Group:     "Games",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processNum(c.Text, c.SenderKey, c.SenderName, c.Channel)
}

const (
	numDefaultMax = 100   // default upper bound when none is given
	numMinMax     = 10    // smallest allowed upper bound
	numMaxMax     = 10000 // largest allowed upper bound
)

// numGame is an open guess-the-number round in one scope (channel or DM).
// lo/hi track the still-possible range so .num status can give a live hint.
type numGame struct {
	secret   int
	attempts int
	lo, hi   int
}

var (
	numGames = map[string]*numGame{} // keyed by game scope
	numMu    sync.Mutex

	numLogBase = games.LogBase("num")
	numLog     = games.NewLog("num")
)

const numUsage = "🎯 .num start [max] | .num <число> | .num top"

// processNum handles ".num" and its sub-verbs.
//
//	.num / .num start [max] – open a game (bot picks 1..max, default 100)
//	.num <n>                – make a guess
//	.num status             – range hint and attempt count
//	.num end                – reveal the secret and close the game
//	.num top                – leaderboard for this scope (last 30 days)
//
// Works in both channels (shared game) and DMs (private game).
func processNum(text, senderKey, senderName, channelName string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, ".num"))
	fields := strings.Fields(rest)

	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	scope := games.Scope(senderKey, channelName)

	switch verb {
	case "", "start":
		return numStart(scope, strings.Join(fields[1:], " "))
	case "status":
		return numStatus(scope)
	case "end":
		return numEnd(scope)
	case "top":
		return games.Top(numLogBase, scope), nil
	default:
		// A bare argument is treated as a guess.
		return numGuess(scope, senderName, fields[0])
	}
}

// numStart opens a round in the scope, picking a secret in 1..max.
func numStart(scope, spec string) ([]string, error) {
	max := numDefaultMax
	if s := strings.TrimSpace(spec); s != "" {
		m, err := strconv.Atoi(s)
		if err != nil {
			return []string{numUsage}, nil
		}
		if m < numMinMax || m > numMaxMax {
			return []string{fmt.Sprintf("max %d–%d", numMinMax, numMaxMax)}, nil
		}
		max = m
	}

	numMu.Lock()
	defer numMu.Unlock()

	if numGames[scope] != nil {
		return []string{"Игра идёт. .num <число>"}, nil
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	numGames[scope] = &numGame{
		secret: r.Intn(max) + 1,
		lo:     1,
		hi:     max,
	}
	return []string{fmt.Sprintf("🎯 Загадал 1–%d. .num <число>", max)}, nil
}

// numGuess records a guess and replies больше/меньше or a win.
func numGuess(scope, senderName, arg string) ([]string, error) {
	guess, err := strconv.Atoi(arg)
	if err != nil {
		return []string{numUsage}, nil
	}

	numMu.Lock()

	g := numGames[scope]
	if g == nil {
		numMu.Unlock()
		return []string{"Игра не начата. .num start"}, nil
	}
	if guess < g.lo || guess > g.hi {
		reply := fmt.Sprintf("Вне диапазона %d–%d", g.lo, g.hi)
		numMu.Unlock()
		return []string{reply}, nil
	}

	g.attempts++
	switch {
	case guess < g.secret:
		g.lo = guess + 1
		numMu.Unlock()
		return []string{"больше"}, nil
	case guess > g.secret:
		g.hi = guess - 1
		numMu.Unlock()
		return []string{"меньше"}, nil
	default:
		attempts, secret := g.attempts, g.secret
		delete(numGames, scope)
		numMu.Unlock()
		games.LogWin(numLog, scope, games.Who(senderName), strconv.Itoa(attempts))
		return []string{fmt.Sprintf("🎯 Угадал за %d! Это %d", attempts, secret)}, nil
	}
}

// numStatus reports the current range and attempt count.
func numStatus(scope string) ([]string, error) {
	numMu.Lock()
	defer numMu.Unlock()

	g := numGames[scope]
	if g == nil {
		return []string{"Игра не начата. .num start"}, nil
	}
	return []string{fmt.Sprintf("Диапазон %d–%d, попыток: %d", g.lo, g.hi, g.attempts)}, nil
}

// numEnd reveals the secret and closes the game.
func numEnd(scope string) ([]string, error) {
	numMu.Lock()
	defer numMu.Unlock()

	g := numGames[scope]
	if g == nil {
		return []string{"Игра не начата. .num start"}, nil
	}
	secret := g.secret
	delete(numGames, scope)
	return []string{fmt.Sprintf("Было %d", secret)}, nil
}
