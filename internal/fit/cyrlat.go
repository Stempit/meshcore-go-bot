package fit

import "strings"

// cyrLatMap maps Cyrillic letters to the Latin (ASCII) look-alikes that are
// pixel-identical in virtually every font. Each Cyrillic letter is 2 bytes in
// UTF-8; its ASCII twin is 1 byte, so every substitution saves exactly one byte
// with no visual change. The set is deliberately conservative: glyphs that only
// resemble a Latin letter (lowercase к, м, т, н, …) are excluded.
var cyrLatMap = map[rune]rune{
	// lowercase
	'а': 'a', 'е': 'e', 'ё': 'e', 'о': 'o', 'с': 'c', 'р': 'p', 'у': 'y', 'х': 'x',
	// uppercase
	'А': 'A', 'В': 'B', 'Е': 'E', 'Ё': 'E', 'К': 'K', 'М': 'M', 'Н': 'H',
	'О': 'O', 'Р': 'P', 'С': 'C', 'Т': 'T', 'У': 'Y', 'Х': 'X',
}

// CyrToLat replaces every mapped Cyrillic letter with its identical-looking
// ASCII twin, leaving all other runes untouched. It is rune-safe.
func CyrToLat(s string) string {
	return strings.Map(func(r rune) rune {
		if l, ok := cyrLatMap[r]; ok {
			return l
		}
		return r
	}, s)
}
