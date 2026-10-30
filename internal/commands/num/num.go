// Package num answers .num: guess-the-number, shared in channels and private in
// DMs, with a 30-day win leaderboard.
package num

import (
	"embed"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stempit/meshcore-go-bot/internal/support/games"
	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "num",
		Triggers:  []string{".num"},
		Match:     registry.Prefix,
		HelpShort: "guess the number",
		HelpLong:  ".num <number> — guess the number (1–100)\n.num start [max] / status / end / top",
		Group:     "Games",
	}
}

// LocalizedHelp returns the per-language long help for ".num help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processNum(c.Lang, c.Rest, c.SenderKey, c.SenderName, c.Channel)
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

// processNum handles ".num" and its sub-verbs.
//
//	.num / .num start [max] – open a game (bot picks 1..max, default 100)
//	.num <n>                – make a guess
//	.num status             – range hint and attempt count
//	.num end                – reveal the secret and close the game
//	.num top                – leaderboard for this scope (last 30 days)
//
// Works in both channels (shared game) and DMs (private game).
// text is Ctx.Rest: everything after the matched ".num" trigger.
func processNum(lang, text, senderKey, senderName, channelName string) ([]string, error) {
	rest := strings.TrimSpace(text)
	fields := strings.Fields(rest)

	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	scope := games.Scope(senderKey, channelName)

	switch verb {
	case "", "start":
		// Guard the slice: a bare ".num" yields an empty fields, so fields[1:]
		// would panic. Only "start [max]" carries an argument after the verb.
		arg := ""
		if len(fields) > 1 {
			arg = strings.Join(fields[1:], " ")
		}
		return numStart(lang, scope, arg)
	case "status":
		return numStatus(lang, scope)
	case "end":
		return numEnd(lang, scope)
	case "top":
		if lines := games.Top(numLogBase, scope); lines != nil {
			return lines, nil
		}
		return []string{tr.T(lang, "top_empty")}, nil
	default:
		// A bare argument is treated as a guess.
		return numGuess(lang, scope, senderName, fields[0])
	}
}

// numStart opens a round in the scope, picking a secret in 1..max.
func numStart(lang, scope, spec string) ([]string, error) {
	max := numDefaultMax
	if s := strings.TrimSpace(spec); s != "" {
		m, err := strconv.Atoi(s)
		if err != nil {
			return []string{tr.T(lang, "usage")}, nil
		}
		if m < numMinMax || m > numMaxMax {
			return []string{tr.T(lang, "max_range", numMinMax, numMaxMax)}, nil
		}
		max = m
	}

	numMu.Lock()
	defer numMu.Unlock()

	if numGames[scope] != nil {
		return []string{tr.T(lang, "already_running")}, nil
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	numGames[scope] = &numGame{
		secret: r.Intn(max) + 1,
		lo:     1,
		hi:     max,
	}
	return []string{tr.T(lang, "started", max)}, nil
}

// numGuess records a guess and replies higher/lower or a win.
func numGuess(lang, scope, senderName, arg string) ([]string, error) {
	guess, err := strconv.Atoi(arg)
	if err != nil {
		return []string{tr.T(lang, "usage")}, nil
	}

	numMu.Lock()

	g := numGames[scope]
	if g == nil {
		numMu.Unlock()
		return []string{tr.T(lang, "not_started")}, nil
	}
	if guess < g.lo || guess > g.hi {
		reply := tr.T(lang, "out_of_range", g.lo, g.hi)
		numMu.Unlock()
		return []string{reply}, nil
	}

	g.attempts++
	switch {
	case guess < g.secret:
		g.lo = guess + 1
		numMu.Unlock()
		return []string{tr.T(lang, "higher")}, nil
	case guess > g.secret:
		g.hi = guess - 1
		numMu.Unlock()
		return []string{tr.T(lang, "lower")}, nil
	default:
		attempts, secret := g.attempts, g.secret
		delete(numGames, scope)
		numMu.Unlock()
		games.LogWin(numLog, scope, games.Who(senderName), strconv.Itoa(attempts))
		return []string{tr.T(lang, "guessed", attempts, secret)}, nil
	}
}

// numStatus reports the current range and attempt count.
func numStatus(lang, scope string) ([]string, error) {
	numMu.Lock()
	defer numMu.Unlock()

	g := numGames[scope]
	if g == nil {
		return []string{tr.T(lang, "not_started")}, nil
	}
	return []string{tr.T(lang, "status", g.lo, g.hi, g.attempts)}, nil
}

// numEnd reveals the secret and closes the game.
func numEnd(lang, scope string) ([]string, error) {
	numMu.Lock()
	defer numMu.Unlock()

	g := numGames[scope]
	if g == nil {
		return []string{tr.T(lang, "not_started")}, nil
	}
	secret := g.secret
	delete(numGames, scope)
	return []string{tr.T(lang, "ended", secret)}, nil
}
