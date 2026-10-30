// Package helpers holds small utilities shared across command plugins, so each
// command folder doesn't re-declare the same logger, path constant, or
// name-clipping logic.
package helpers

import "strings"

// HistoryDir is the base directory the .last/.top/.stat readers scan for
// channel and DM message history (written by the bot's history logger).
const HistoryDir = "log/history"

// SingleLine strips CR and LF from s, replacing them with spaces. Anything a
// remote peer controls (message text, node names) must pass through this
// before being written to a line-oriented record: raw newlines would forge
// additional entries that the readers parse back as authentic history.
func SingleLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// ClipRunes shortens s to at most maxRunes runes, appending an ellipsis when it
// had to cut. It counts runes (not bytes) so multi-byte Cyrillic names stay
// readable; callers that need a byte budget must clip by bytes themselves.
func ClipRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
