// Package helpers holds small utilities shared across command plugins, so each
// command folder doesn't re-declare the same logger, path constant, or
// name-clipping logic.
package helpers

// HistoryDir is the base directory the .last/.top/.stat readers scan for
// channel and DM message history (written by the bot's history logger).
const HistoryDir = "log/history"

// ClipRunes shortens s to at most maxRunes runes, appending an ellipsis when it
// had to cut. It counts runes (not bytes) so multi-byte Cyrillic names stay
// within the 140-byte mesh message limit.
func ClipRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
