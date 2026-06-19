package games

import (
	_ "embed"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed words_ru.txt
var hangWordsRaw string

// hangWords is the playable word bank: normalized, length-filtered, loaded once.
var hangWords = loadHangWords(hangWordsRaw)

const (
	hangLives  = 6 // wrong guesses allowed
	hangMinLen = 4 // shortest playable word (runes)
	hangMaxLen = 8 // longest, so mask + status fits the 140-byte limit
)

// hangGame is an open hangman round in one scope (channel or DM). In a channel
// it is collaborative: shared word and shared lives.
type hangGame struct {
	word    []rune        // normalized target word
	guessed map[rune]bool // letters tried (correct or wrong)
	lives   int
}

var (
	hangGames = map[string]*hangGame{} // keyed by game scope
	hangMu    sync.Mutex
)

const hangUsage = "🪢 .hang start | .hang <буква> | .hang top"

// ProcessHangCommand handles ".hang" and its sub-verbs.
//
//	.hang / .hang start – pick a random word and open a game
//	.hang <буква>       – guess one letter
//	.hang <слово>       – guess the whole word
//	.hang status        – show mask, used letters, lives
//	.hang end           – give up and reveal the word
//	.hang top           – leaderboard for this scope (last 30 days)
//
// Works in both channels (collaborative) and DMs (solo).
func ProcessHangCommand(text, senderKey, senderName, channelName string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, ".hang"))
	fields := strings.Fields(rest)

	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	scope := gameScope(senderKey, channelName)

	switch verb {
	case "", "start":
		return hangStart(scope)
	case "status":
		return hangStatus(scope)
	case "end":
		return hangEnd(scope)
	case "top":
		return gameTop(hangLogBase, scope), nil
	default:
		// Anything else is a letter or whole-word guess.
		return hangGuess(scope, senderName, fields[0])
	}
}

// hangStart picks a random word and opens a game in the scope.
func hangStart(scope string) ([]string, error) {
	if len(hangWords) == 0 {
		return []string{"Словарь пуст"}, nil
	}

	hangMu.Lock()
	defer hangMu.Unlock()

	if hangGames[scope] != nil {
		return []string{"Игра идёт. .hang <буква>"}, nil
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	g := &hangGame{
		word:    []rune(hangWords[r.Intn(len(hangWords))]),
		guessed: map[rune]bool{},
		lives:   hangLives,
	}
	hangGames[scope] = g
	return []string{hangRender(g)}, nil
}

// hangGuess applies a single-letter or whole-word guess.
func hangGuess(scope, senderName, arg string) ([]string, error) {
	norm := normalizeRu(arg)
	if norm == "" {
		return []string{"Только русские буквы"}, nil
	}
	letters := []rune(norm)

	hangMu.Lock()

	g := hangGames[scope]
	if g == nil {
		hangMu.Unlock()
		return []string{"Игра не начата. .hang start"}, nil
	}

	// Whole-word guess.
	if len(letters) > 1 {
		if string(letters) == string(g.word) {
			word := string(g.word)
			delete(hangGames, scope)
			hangMu.Unlock()
			logWin(hangLog, scope, gameWho(senderName), word)
			return []string{"🎉 " + strings.ToUpper(word)}, nil
		}
		return hangPenalize(scope, g)
	}

	// Single-letter guess.
	letter := letters[0]
	if g.guessed[letter] {
		reply := fmt.Sprintf("Буква '%c' уже была", letter)
		hangMu.Unlock()
		return []string{reply}, nil
	}
	g.guessed[letter] = true

	if !runeIn(g.word, letter) {
		return hangPenalize(scope, g)
	}

	if hangSolved(g) {
		word := string(g.word)
		delete(hangGames, scope)
		hangMu.Unlock()
		logWin(hangLog, scope, gameWho(senderName), word)
		return []string{"🎉 " + strings.ToUpper(word)}, nil
	}

	reply := hangRender(g)
	hangMu.Unlock()
	return []string{reply}, nil
}

// hangPenalize docks a life for a wrong guess and ends the game on a loss.
// The caller must hold hangMu; hangPenalize releases it.
func hangPenalize(scope string, g *hangGame) ([]string, error) {
	g.lives--
	if g.lives <= 0 {
		word := string(g.word)
		delete(hangGames, scope)
		hangMu.Unlock()
		return []string{"💀 Было: " + strings.ToUpper(word)}, nil
	}
	reply := hangRender(g)
	hangMu.Unlock()
	return []string{reply}, nil
}

// hangStatus shows the current board.
func hangStatus(scope string) ([]string, error) {
	hangMu.Lock()
	defer hangMu.Unlock()

	g := hangGames[scope]
	if g == nil {
		return []string{"Игра не начата. .hang start"}, nil
	}
	return []string{hangRender(g)}, nil
}

// hangEnd gives up and reveals the word.
func hangEnd(scope string) ([]string, error) {
	hangMu.Lock()
	defer hangMu.Unlock()

	g := hangGames[scope]
	if g == nil {
		return []string{"Игра не начата. .hang start"}, nil
	}
	word := string(g.word)
	delete(hangGames, scope)
	return []string{"💀 Было: " + strings.ToUpper(word)}, nil
}

// hangRender draws the masked word, the wrong letters, and remaining lives,
// e.g. "к__к_  ✗б,о  ❤4". The mask uses no spaces to save bytes.
func hangRender(g *hangGame) string {
	var mask strings.Builder
	for _, r := range g.word {
		if g.guessed[r] {
			mask.WriteRune(r)
		} else {
			mask.WriteRune('_')
		}
	}

	var wrong []string
	for r := range g.guessed {
		if !runeIn(g.word, r) {
			wrong = append(wrong, string(r))
		}
	}
	sort.Strings(wrong)

	out := mask.String()
	if len(wrong) > 0 {
		out += "  ✗" + strings.Join(wrong, ",")
	}
	return fmt.Sprintf("%s  ❤%d", out, g.lives)
}

// hangSolved reports whether every letter of the word has been guessed.
func hangSolved(g *hangGame) bool {
	for _, r := range g.word {
		if !g.guessed[r] {
			return false
		}
	}
	return true
}

// runeIn reports whether r appears in rs.
func runeIn(rs []rune, r rune) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// normalizeRu lowercases s, folds ё→е, and keeps only Russian letters. Used for
// both word-bank entries and player guesses so they compare consistently.
func normalizeRu(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if r == 'ё' {
			r = 'е'
		}
		if r >= 'а' && r <= 'я' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// loadHangWords normalizes and length-filters the embedded word list.
func loadHangWords(raw string) []string {
	var words []string
	for _, line := range strings.Split(raw, "\n") {
		w := normalizeRu(line)
		if n := len([]rune(w)); n < hangMinLen || n > hangMaxLen {
			continue
		}
		words = append(words, w)
	}
	return words
}
