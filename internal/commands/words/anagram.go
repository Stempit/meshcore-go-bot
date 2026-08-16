package words

import (
	"bufio"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"meshbot/internal/config"
	"meshbot/internal/support/games"
	"meshbot/internal/support/helpers"
	"meshbot/internal/support/i18n"
)

// wordsLang resolves the global bot language for code paths without a Ctx
// (the round-timeout callback fires outside any request).
func wordsLang() string {
	if c := config.Get(); c != nil && c.Language != "" {
		return c.Language
	}
	return i18n.Base
}

// russian words from http://blog.kislenko.net/show.php?id=1678

const (
	wordsDefRoundSec = 300                    // countdown length when unset (5 min)
	wordsDefMinSub   = 3                      // shortest valid submission (runes)
	wordsDefBaseMin  = 8                      // shortest base word (runes)
	wordsDefBaseMax  = 10                     // longest base word (runes)
	wordsDefDictPath = "words_anagram_ru.txt" // runtime dictionary in CWD
)

// wordsGame is an open round in one scope (channel or DM). In a channel it is
// collaborative-competitive: one shared base word, but each player keeps their
// own score and the highest scorer at timeout wins.
type wordsGame struct {
	base     []rune          // normalized base word, e.g. []rune("кальвадос")
	avail    map[rune]int    // letter -> count available in base (multiset)
	found    map[string]bool // words already claimed this round (anti-duplicate)
	scores   map[string]int  // player name -> total score
	timer    *time.Timer     // countdown; reset on every command
	deadline time.Time       // wall-clock expiry; drives status + stale-fire guard
	token    uint64          // round identity; guards stale timer fires
}

var (
	wordsGames = map[string]*wordsGame{} // keyed by game scope
	wordsMu    sync.Mutex
	wordsToken uint64 // monotonic round-id source, guarded by wordsMu

	wordsLogBase = games.LogBase("words")
	wordsLog     = games.NewLog("words")

	// Indexes loaded once at startup by loadWordsDict, before any game runs, so
	// concurrent reads from worker goroutines need no lock (load-once pattern,
	// like the embedded hang word bank).
	wordsValid map[string]struct{} // validation set (submission membership)
	wordsPool  []string            // base-word pool (BaseMin..BaseMax runes)
)

// processWords handles ".words" and its sub-verbs.
//
//	.words / .words start – pick a base word and open a round
//	.words <слово>        – submit a word built from the base letters
//	.words status         – base word, time left, your score
//	.words end            – close the round early and show results now
//	.words top            – leaderboard for this scope (last 30 days)
//
// Works in both channels (shared base word, per-player scores) and DMs (solo).
func processWords(lang, text, senderKey, senderName, channelName string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, ".words"))
	fields := strings.Fields(rest)

	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	scope := games.Scope(senderKey, channelName)

	switch verb {
	case "", "start":
		return wordsStart(lang, scope)
	case "status":
		return wordsStatus(lang, scope, games.Who(senderName))
	case "end":
		return wordsEnd(lang, scope)
	case "top":
		return games.Top(wordsLogBase, scope), nil
	default:
		return wordsSubmit(lang, scope, games.Who(senderName), fields[0])
	}
}

// wordsStart opens a round in the scope, picking a base word from the pool.
func wordsStart(lang, scope string) ([]string, error) {
	if len(wordsPool) == 0 {
		return []string{tr.T(lang, "dict_unavailable")}, nil
	}

	wordsMu.Lock()
	defer wordsMu.Unlock()

	// A start on a live round just refreshes its timer (requirement 6) and tells
	// the caller the game is already running.
	if g := wordsGames[scope]; g != nil {
		wordsResetLocked(g)
		return []string{tr.T(lang, "already_running")}, nil
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	base := []rune(wordsPool[r.Intn(len(wordsPool))])

	wordsToken++
	g := &wordsGame{
		base:   base,
		avail:  runeCounts(base),
		found:  map[string]bool{},
		scores: map[string]int{},
		token:  wordsToken,
	}
	ttl := wordsRoundDur()
	g.deadline = time.Now().Add(ttl)
	tok := g.token
	g.timer = time.AfterFunc(ttl, func() { wordsOnTimeout(scope, tok) })
	wordsGames[scope] = g

	slog.Info("words round started", "scope", scope, "base", string(base))
	return []string{tr.T(lang, "started", strings.ToUpper(string(base)), int(ttl.Minutes()))}, nil
}

// wordsSubmit validates a submission and, on success, scores it. The validation
// order is cheapest-first; the first failure decides the reply.
func wordsSubmit(lang, scope, who, arg string) ([]string, error) {
	norm := games.NormalizeRu(arg)
	if norm == "" {
		return []string{tr.T(lang, "only_russian")}, nil
	}
	word := []rune(norm)

	wordsMu.Lock()

	g := wordsGames[scope]
	if g == nil {
		wordsMu.Unlock()
		return []string{tr.T(lang, "not_started")}, nil
	}
	wordsResetLocked(g) // any command keeps the round alive (requirement 6)

	switch {
	case len(word) < wordsMinSub():
		wordsMu.Unlock()
		return []string{tr.T(lang, "too_short")}, nil
	case norm == string(g.base):
		wordsMu.Unlock()
		return []string{tr.T(lang, "base_itself")}, nil
	case g.found[norm]:
		wordsMu.Unlock()
		return []string{tr.T(lang, "already_found")}, nil
	case !buildable(word, g.avail):
		wordsMu.Unlock()
		return []string{tr.T(lang, "not_found")}, nil
	}
	if _, ok := wordsValid[norm]; !ok {
		wordsMu.Unlock()
		return []string{tr.T(lang, "not_found")}, nil
	}

	// Score is super-linear in rune length so long finds pay off. Use rune
	// length, not len(norm): a Cyrillic letter is 2 bytes, so len would square
	// the byte count instead.
	n := len(word)
	score := n * n
	g.found[norm] = true
	g.scores[who] += score
	wordsMu.Unlock()

	slog.Info("words find", "scope", scope, "player", who, "word", norm, "score", score)
	return []string{tr.T(lang, "scored", score)}, nil
}

// wordsStatus shows the base word, time left, and the caller's score. It also
// resets the timer (requirement 6).
func wordsStatus(lang, scope, who string) ([]string, error) {
	wordsMu.Lock()
	defer wordsMu.Unlock()

	g := wordsGames[scope]
	if g == nil {
		return []string{tr.T(lang, "not_started")}, nil
	}
	wordsResetLocked(g)

	left := time.Until(g.deadline)
	if left < 0 {
		left = 0
	}
	m := int(left / time.Minute)
	s := int(left/time.Second) % 60
	return []string{tr.T(lang, "status", strings.ToUpper(string(g.base)), m, s, g.scores[who])}, nil
}

// wordsEnd closes the round early and returns the results synchronously. If the
// round is already gone (a timeout finalized it microseconds earlier), it is
// treated as "no active round" rather than double-finalized.
func wordsEnd(lang, scope string) ([]string, error) {
	wordsMu.Lock()
	defer wordsMu.Unlock()

	g := wordsGames[scope]
	if g == nil {
		return []string{tr.T(lang, "not_started")}, nil
	}
	g.timer.Stop()
	return wordsFinalizeLocked(lang, scope, g), nil
}

// wordsOnTimeout is the AfterFunc callback. It bails on a stale fire — the round
// was ended/restarted (token mismatch) or a command reset the timer in the
// window between this fire and the lock (deadline still in the future). The
// token never changes on a reset, so the deadline guard is what keeps a reset
// that races the fire from ending the round early.
func wordsOnTimeout(scope string, token uint64) {
	wordsMu.Lock()
	g := wordsGames[scope]
	if g == nil || g.token != token || time.Now().Before(g.deadline) {
		wordsMu.Unlock()
		return
	}
	results := wordsFinalizeLocked(wordsLang(), scope, g)
	wordsMu.Unlock()

	// Resolve-and-enqueue may be slow; never hold wordsMu across it.
	games.Announce(scope, results)
}

// wordsFinalizeLocked ranks players, records the winner(s), deletes the round,
// and renders the results. The caller must hold wordsMu.
func wordsFinalizeLocked(lang, scope string, g *wordsGame) []string {
	base := strings.ToUpper(string(g.base))
	delete(wordsGames, scope)

	type playerScore struct {
		name  string
		score int
	}
	var ranked []playerScore
	for name, score := range g.scores {
		if score > 0 {
			ranked = append(ranked, playerScore{name, score})
		}
	}
	if len(ranked) == 0 {
		slog.Info("words round ended", "scope", scope, "winner", "", "score", 0)
		return []string{tr.T(lang, "nobody", base)}
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].name < ranked[j].name
	})

	// Build the ranked line to a byte budget (like games.Top): append players
	// while they fit, then stop — never split, which would silently truncate.
	line := "🏆 "
	shown := 0
	for _, p := range ranked {
		if shown >= games.TopMax {
			break
		}
		part := fmt.Sprintf("%s %d", helpers.ClipRunes(p.name, games.NameMaxRune), p.score)
		sep := ""
		if shown > 0 {
			sep = " · "
		}
		if len(line)+len(sep)+len(part) > games.TopBytes {
			break
		}
		line += sep + part
		shown++
	}

	// Record every top scorer as a winner (tie handling like .dice).
	top := ranked[0].score
	for _, p := range ranked {
		if p.score != top {
			break
		}
		games.LogWin(wordsLog, scope, p.name, strconv.Itoa(p.score))
	}
	slog.Info("words round ended", "scope", scope, "winner", ranked[0].name, "score", top)

	return []string{tr.T(lang, "results_header", base), line}
}

// wordsResetLocked refreshes the countdown to its full length. The caller must
// hold wordsMu. Reset is safe without draining: AfterFunc timers have no channel.
func wordsResetLocked(g *wordsGame) {
	ttl := wordsRoundDur()
	g.deadline = time.Now().Add(ttl)
	g.timer.Reset(ttl)
}

// buildable reports whether word can be assembled from the base letters: a
// multiset (not set) check, so one base letter backs at most one submission
// letter (e.g. "скос" needs two с but "кальвадос" has only one → rejected).
func buildable(word []rune, avail map[rune]int) bool {
	need := make(map[rune]int, len(word))
	for _, r := range word {
		need[r]++
		if need[r] > avail[r] {
			return false
		}
	}
	return true
}

// runeCounts returns the per-rune multiset of rs.
func runeCounts(rs []rune) map[rune]int {
	m := make(map[rune]int, len(rs))
	for _, r := range rs {
		m[r]++
	}
	return m
}

// loadWordsDict loads the runtime dictionary into the validation set and the
// base-word pool. It is defensive: every line is run through games.NormalizeRu
// and the ≥3-rune / pure-Cyrillic filter, so pointing it at a raw singular.txt
// also works. On a missing/empty file it logs a warning, leaves the indexes
// empty (so .words start replies "Словарь недоступен"), and returns the error —
// the rest of the bot keeps running. Called once at startup, from Configure.
func loadWordsDict(path string) error {
	if path == "" {
		path = wordsDefDictPath
	}
	f, err := os.Open(path)
	if err != nil {
		slog.Warn("words: dictionary not loaded", "path", path, "error", err)
		return err
	}
	defer f.Close()

	bmin, bmax := wordsBaseMin(), wordsBaseMax()
	valid := make(map[string]struct{})
	var pool []string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		w := games.NormalizeRu(sc.Text())
		n := len([]rune(w))
		if n < wordsDefMinSub {
			continue
		}
		valid[w] = struct{}{}
		if n >= bmin && n <= bmax {
			pool = append(pool, w)
		}
	}
	if err := sc.Err(); err != nil {
		slog.Warn("words: error reading dictionary", "path", path, "error", err)
		return err
	}

	wordsValid = valid
	wordsPool = pool
	slog.Info("words dictionary loaded", "path", path, "words", len(valid), "pool", len(pool))
	return nil
}

// Config accessors with use-site defaults (no central config defaulter); they
// read the plugin's own decoded config section.

func wordsRoundDur() time.Duration {
	if s := conf.RoundSec; s > 0 {
		return time.Duration(s) * time.Second
	}
	return wordsDefRoundSec * time.Second
}

func wordsMinSub() int {
	if n := conf.MinSubLen; n > 0 {
		return n
	}
	return wordsDefMinSub
}

func wordsBaseMin() int {
	if n := conf.BaseMinLen; n > 0 {
		return n
	}
	return wordsDefBaseMin
}

func wordsBaseMax() int {
	if n := conf.BaseMaxLen; n > 0 {
		return n
	}
	return wordsDefBaseMax
}
