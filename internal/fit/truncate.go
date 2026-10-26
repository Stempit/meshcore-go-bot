package fit

import (
	"strings"
	"unicode/utf8"
)

// cutOnBoundary returns the largest index ≤ limit that lands on a UTF-8 rune
// boundary, preferring a space in the back half of the range so cuts fall on
// word boundaries when possible.
func cutOnBoundary(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	if limit < 0 {
		return 0
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if idx := strings.LastIndex(s[:cut], " "); idx > limit/2 {
		cut = idx
	}
	return cut
}

// truncate cuts s to at most max bytes on a rune boundary, appending the
// ellipsis (which also counts toward max).
func truncate(s string, max int, ellipsis string) string {
	if len(s) <= max {
		return s
	}
	limit := max - len(ellipsis)
	if limit < 0 {
		limit = 0
		ellipsis = ""
	}
	return strings.TrimSpace(s[:cutOnBoundary(s, limit)]) + ellipsis
}

// hardCap is the final, ellipsis-free safety cut applied to every returned
// message. It is a no-op unless a message is still over max (e.g. a prefix
// longer than the budget).
func hardCap(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:cutOnBoundary(s, max)]
}
