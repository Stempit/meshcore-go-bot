package games

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"meshbot/internal/historylog"
)

// Shared helpers for the text games (.num, .hang). Each game keeps its own
// in-memory state but reuses the same scope keying and the historylog package
// for persistent win records that feed the per-game leaderboards.

const (
	gamesBaseDir    = "log/games" // root for per-game results logs
	gameTopMax      = 5           // players listed in a leaderboard (mesh size)
	gameTopDays     = 30          // leaderboard window
	gameTopBytes    = 130         // byte budget for a one-line leaderboard reply
	gameNameMaxRune = 12          // player names are clipped to fit the mesh limit
)

var (
	numLogBase  = filepath.Join(gamesBaseDir, "num")
	hangLogBase = filepath.Join(gamesBaseDir, "hang")

	// Win records are written through historylog (no dedicated package): each
	// game type gets its own base dir, and the scope string is used as the
	// "channel" name so both channel and DM games share one reader/writer.
	numLog  = historylog.New(numLogBase)
	hangLog = historylog.New(hangLogBase)
)

// gameScope keys a game by its conversation: the channel name for channel games
// (shared by everyone in the channel) or the sender's key for DMs (private).
func gameScope(senderKey, channelName string) string {
	if s := strings.TrimSpace(channelName); s != "" {
		return s
	}
	return "dm:" + senderKey
}

// gameWho returns a usable player label for win records, falling back when the
// name is empty (e.g. an unresolved contact).
func gameWho(senderName string) string {
	if n := strings.TrimSpace(senderName); n != "" {
		return n
	}
	return "?"
}

// logWin appends a win record for the scope. Logging failures are non-fatal:
// the game already finished, so a missing leaderboard entry just gets dropped.
func logWin(logger *historylog.Logger, scope, winner, detail string) {
	if err := logger.LogChannelIncoming(scope, winner, detail, time.Now()); err != nil {
		slog.Warn("game: failed to log win", "scope", scope, "error", err)
	}
}

// gameTop renders the leaderboard for one scope: wins per player over the last
// gameTopDays, ranked descending, capped to fit a single mesh message.
func gameTop(base, scope string) []string {
	entries, err := historylog.ReadAllChannel(base, scope)
	if err != nil {
		slog.Warn("game: failed to read leaderboard", "scope", scope, "error", err)
		return []string{"Пока никто не выигрывал"}
	}

	cutoff := time.Now().AddDate(0, 0, -gameTopDays)
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
		return []string{"Пока никто не выигрывал"}
	}

	sort.SliceStable(order, func(i, j int) bool {
		return counts[order[i]] > counts[order[j]]
	})

	// Append players while they fit the byte budget and the player cap.
	line := "🏆 "
	shown := 0
	for _, name := range order {
		if shown >= gameTopMax {
			break
		}
		part := fmt.Sprintf("%s %d", clipGameName(name), counts[name])
		sep := ""
		if shown > 0 {
			sep = ", "
		}
		if len(line)+len(sep)+len(part) > gameTopBytes {
			break
		}
		line += sep + part
		shown++
	}
	return []string{line}
}

// clipGameName shortens a player name to gameNameMaxRune runes (adding an
// ellipsis) so a five-player leaderboard still fits the 140-byte mesh limit.
func clipGameName(name string) string {
	r := []rune(name)
	if len(r) <= gameNameMaxRune {
		return name
	}
	return string(r[:gameNameMaxRune]) + "…"
}
