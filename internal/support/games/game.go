// Package games is the shared support layer for the text-game command plugins
// (.dice, .num, .hang, .words). The game logic lives in each command folder
// under internal/commands/<game>; this package holds only what those plugins
// share: scope keying, the win-record leaderboard, the round-result announcer,
// and Russian text normalization.
package games

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stempit/meshcore-go-bot/internal/support/helpers"
	"github.com/stempit/meshcore-go-bot/internal/support/historylog"
)

// BaseDir is the root for per-game result logs (leaderboards).
const BaseDir = "log/games"

const (
	TopMax      = 5   // players listed in a leaderboard (mesh size)
	TopDays     = 30  // leaderboard window (days)
	TopBytes    = 130 // byte budget for a one-line leaderboard reply
	NameMaxRune = 12  // player names are clipped to fit the mesh limit
)

// NewLog returns the historylog.Logger a game writes win records through. Win
// records have no dedicated package: each game gets its own base dir and uses
// the scope string as the "channel" name so channel and DM games share one
// reader/writer.
func NewLog(game string) *historylog.Logger { return historylog.New(LogBase(game)) }

// LogBase returns the base dir for a game's win records (BaseDir/<game>), used
// when reading the leaderboard via Top.
func LogBase(game string) string { return filepath.Join(BaseDir, game) }

// Announcer pushes unsolicited game messages (e.g. round results from a timer)
// to a scope. It decouples the games layer from the send layer: games never
// import dispatch/meshcore, and the real implementation is injected at startup.
type Announcer func(scope string, msgs []string)

var announce Announcer = func(string, []string) {} // no-op until wired

// SetAnnouncer installs the announcer. Called once at startup from the
// composition root; a nil argument is ignored so the no-op stays in place.
func SetAnnouncer(a Announcer) {
	if a != nil {
		announce = a
	}
}

// Announce pushes msgs to a scope via the injected announcer. Game plugins call
// this from timer goroutines, where there is no incoming message to reply to.
func Announce(scope string, msgs []string) { announce(scope, msgs) }

// Scope keys a game by its conversation: the channel name for channel games
// (shared by everyone in the channel) or the sender's key for DMs (private).
func Scope(senderKey, channelName string) string {
	if s := strings.TrimSpace(channelName); s != "" {
		return s
	}
	return "dm:" + senderKey
}

// Who returns a usable player label for win records, falling back when the
// name is empty (e.g. an unresolved contact).
func Who(senderName string) string {
	if n := strings.TrimSpace(senderName); n != "" {
		return n
	}
	return "?"
}

// LogWin appends a win record for the scope. Logging failures are non-fatal:
// the game already finished, so a missing leaderboard entry just gets dropped.
func LogWin(logger *historylog.Logger, scope, winner, detail string) {
	if err := logger.LogChannelIncoming(scope, winner, detail, time.Now()); err != nil {
		slog.Warn("game: failed to log win", "scope", scope, "error", err)
	}
}

// Top renders the leaderboard for one scope: wins per player over the last
// TopDays, ranked descending, capped to fit a single mesh message. base is the
// game's LogBase. Returns nil when nothing is recorded — the caller localizes
// the empty case from its own catalog (games never had the language).
func Top(base, scope string) []string {
	entries, err := historylog.ReadAllChannel(base, scope)
	if err != nil {
		slog.Warn("game: failed to read leaderboard", "scope", scope, "error", err)
		return nil
	}

	cutoff := time.Now().AddDate(0, 0, -TopDays)
	counts := make(map[string]int)
	var order []string // first-seen order for stable ties
	for _, e := range entries {
		if e.Time.Before(cutoff) {
			continue
		}
		if _, seen := counts[e.Sender]; !seen {
			order = append(order, e.Sender)
		}
		counts[e.Sender]++
	}
	if len(order) == 0 {
		return nil
	}

	sort.SliceStable(order, func(i, j int) bool {
		return counts[order[i]] > counts[order[j]]
	})

	// Append players while they fit the byte budget and the player cap.
	line := "🏆 "
	shown := 0
	for _, name := range order {
		if shown >= TopMax {
			break
		}
		part := fmt.Sprintf("%s %d", helpers.ClipRunes(name, NameMaxRune), counts[name])
		sep := ""
		if shown > 0 {
			sep = ", "
		}
		if len(line)+len(sep)+len(part) > TopBytes {
			break
		}
		line += sep + part
		shown++
	}
	return []string{line}
}

// NormalizeRu lowercases s, folds ё→е, and keeps only Russian letters. The word
// games (.hang, .words) use it for both word-bank entries and player guesses so
// they compare consistently.
func NormalizeRu(s string) string {
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

// NormalizeEn lowercases s and keeps only English letters. The English-language
// dictionary variant of .words uses it for both word-bank entries and player
// guesses so they compare consistently.
func NormalizeEn(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
