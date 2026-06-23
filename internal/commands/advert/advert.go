// Package advert parses the .advert command. Unlike the registry command
// plugins, .advert is handled in the dispatch layer (it drives the MeshCore client's
// SendSelfAdvert, which command plugins have no access to), so this package
// exposes only a parser that dispatch calls before registry.Dispatch.
package advert

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
