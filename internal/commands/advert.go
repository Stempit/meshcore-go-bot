package commands

import "strings"

// ParseAdvertCommand reports whether text is a .advert command and whether
// it requests flood mode. Returns ok=false when the text is not a .advert command.
func ParseAdvertCommand(text string) (flood bool, ok bool) {
	lower := strings.ToLower(strings.TrimSpace(text))
	if !strings.HasPrefix(lower, ".advert") {
		return false, false
	}
	arg := strings.TrimSpace(strings.TrimPrefix(lower, ".advert"))
	return arg != "direct", true
}
