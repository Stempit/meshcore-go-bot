// Package hang answers .hang: hangman from an embedded word bank, Russian or
// English depending on the bot's Language setting.
// Channels are collaborative (shared word and lives), DMs solo; 30-day leaderboard.
package hang

import (
	"embed"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stempit/meshcore-go-bot/internal/config"
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
		Name:      "hang",
		Triggers:  []string{".hang"},
		Match:     registry.Prefix,
		HelpShort: "hangman",
		HelpLong:  ".hang <letter> — hangman\n.hang start / <word> / status / end / top",
		Group:     "Games",
	}
}

// LocalizedHelp returns the per-language long help for ".hang help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processHang(c.Lang, c.Text, c.SenderKey, c.SenderName, c.Channel)
}

//go:embed words_ru.txt
var hangWordsRuRaw string

//go:embed words_en.txt
var hangWordsEnRaw string

// hangWords is the playable word bank: normalized, length-filtered, loaded
// once by Configure after config.Get().Language is known.
var hangWords []string

const (
	hangLives  = 6 // wrong guesses allowed
	hangMinLen = 4 // shortest playable word (runes)
	hangMaxLen = 8 // longest, so mask + status fits the 140-byte limit
)

// hangLang resolves the global bot language, same convention as words.wordsLang.
func hangLang() string {
	if c := config.Get(); c != nil && c.Language != "" {
		return c.Language
	}
	return i18n.Base
}

// hangNormalize normalizes s for the word bank matching lang: Russian
// (ё-folded Cyrillic) for "ru", English (a-z) otherwise.
func hangNormalize(lang, s string) string {
	if lang == "ru" {
		return games.NormalizeRu(s)
	}
	return games.NormalizeEn(s)
}

func (plugin) ConfigKey() string { return "hang" }

// Configure loads the word bank matching the bot's configured language. Hang
// has no config fields of its own; this exists only to run after config.Load
// so Language is in effect. It runs once at startup, before any round.
func (plugin) Configure(config.Sectioner) error {
	lang := hangLang()
	raw := hangWordsEnRaw
	if lang == "ru" {
		raw = hangWordsRuRaw
	}
	hangWords = loadHangWords(lang, raw)
	return nil
}

func (plugin) Enabled() bool { return true }

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

	hangLogBase = games.LogBase("hang")
	hangLog     = games.NewLog("hang")
)

// processHang handles ".hang" and its sub-verbs.
//
//	.hang / .hang start – pick a random word and open a game
//	.hang <буква>       – guess one letter
//	.hang <слово>       – guess the whole word
//	.hang status        – show mask, used letters, lives
//	.hang end           – give up and reveal the word
//	.hang top           – leaderboard for this scope (last 30 days)
//
// Works in both channels (collaborative) and DMs (solo).
func processHang(lang, text, senderKey, senderName, channelName string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, ".hang"))
	fields := strings.Fields(rest)

	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	scope := games.Scope(senderKey, channelName)

	switch verb {
	case "", "start":
		return hangStart(lang, scope)
	case "status":
		return hangStatus(lang, scope)
	case "end":
		return hangEnd(lang, scope)
	case "top":
		return games.Top(hangLogBase, scope), nil
	default:
		// Anything else is a letter or whole-word guess.
		return hangGuess(lang, scope, senderName, fields[0])
	}
}

// hangStart picks a random word and opens a game in the scope.
func hangStart(lang, scope string) ([]string, error) {
	if len(hangWords) == 0 {
		return []string{tr.T(lang, "dict_empty")}, nil
	}

	hangMu.Lock()
	defer hangMu.Unlock()

	if hangGames[scope] != nil {
		return []string{tr.T(lang, "already_running")}, nil
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
func hangGuess(lang, scope, senderName, arg string) ([]string, error) {
	norm := hangNormalize(lang, arg)
	if norm == "" {
		return []string{tr.T(lang, "invalid_chars")}, nil
	}
	letters := []rune(norm)

	hangMu.Lock()

	g := hangGames[scope]
	if g == nil {
		hangMu.Unlock()
		return []string{tr.T(lang, "not_started")}, nil
	}

	// Whole-word guess.
	if len(letters) > 1 {
		if string(letters) == string(g.word) {
			word := string(g.word)
			delete(hangGames, scope)
			hangMu.Unlock()
			games.LogWin(hangLog, scope, games.Who(senderName), word)
			return []string{tr.T(lang, "won", strings.ToUpper(word))}, nil
		}
		return hangPenalize(lang, scope, g)
	}

	// Single-letter guess.
	letter := letters[0]
	if g.guessed[letter] {
		reply := tr.T(lang, "letter_used", letter)
		hangMu.Unlock()
		return []string{reply}, nil
	}
	g.guessed[letter] = true

	if !runeIn(g.word, letter) {
		return hangPenalize(lang, scope, g)
	}

	if hangSolved(g) {
		word := string(g.word)
		delete(hangGames, scope)
		hangMu.Unlock()
		games.LogWin(hangLog, scope, games.Who(senderName), word)
		return []string{tr.T(lang, "won", strings.ToUpper(word))}, nil
	}

	reply := hangRender(g)
	hangMu.Unlock()
	return []string{reply}, nil
}

// hangPenalize docks a life for a wrong guess and ends the game on a loss.
// The caller must hold hangMu; hangPenalize releases it.
func hangPenalize(lang, scope string, g *hangGame) ([]string, error) {
	g.lives--
	if g.lives <= 0 {
		word := string(g.word)
		delete(hangGames, scope)
		hangMu.Unlock()
		return []string{tr.T(lang, "lost", strings.ToUpper(word))}, nil
	}
	reply := hangRender(g)
	hangMu.Unlock()
	return []string{reply}, nil
}

// hangStatus shows the current board.
func hangStatus(lang, scope string) ([]string, error) {
	hangMu.Lock()
	defer hangMu.Unlock()

	g := hangGames[scope]
	if g == nil {
		return []string{tr.T(lang, "not_started")}, nil
	}
	return []string{hangRender(g)}, nil
}

// hangEnd gives up and reveals the word.
func hangEnd(lang, scope string) ([]string, error) {
	hangMu.Lock()
	defer hangMu.Unlock()

	g := hangGames[scope]
	if g == nil {
		return []string{tr.T(lang, "not_started")}, nil
	}
	word := string(g.word)
	delete(hangGames, scope)
	return []string{tr.T(lang, "lost", strings.ToUpper(word))}, nil
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

// loadHangWords normalizes and length-filters the embedded word list matching lang.
func loadHangWords(lang, raw string) []string {
	var words []string
	for _, line := range strings.Split(raw, "\n") {
		w := hangNormalize(lang, line)
		if n := len([]rune(w)); n < hangMinLen || n > hangMaxLen {
			continue
		}
		words = append(words, w)
	}
	return words
}
