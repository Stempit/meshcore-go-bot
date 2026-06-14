package shrink

import (
	"fmt"
	"strings"
)

const (
	// maxParts caps a single split message at 9 wire parts.
	maxParts = 9
	// markerReserve is the byte cost of the largest series marker, " [9/9]".
	// Since N ≤ 9 the marker is always single-digit on both sides.
	markerReserve = 6
)

// split breaks text into up to maxParts rune-safe chunks, each suffixed with a
// "[i/N]" series marker, every part fitting within max bytes. head reserves
// bytes on the first chunk only (for a send-layer prefix). When the text cannot
// fit in maxParts, the last part is truncated with an ellipsis.
func split(text string, max, head int) []string {
	text = strings.TrimSpace(text)
	chunkMax := max - markerReserve

	var chunks []string
	s := text
	for len(s) > 0 {
		budget := chunkMax
		if len(chunks) == 0 {
			budget -= head
		}
		if budget < 1 {
			budget = 1
		}

		// Last allowed part with text remaining: truncate the rest to fit.
		if len(chunks) == maxParts-1 && len(s) > budget {
			chunks = append(chunks, truncate(s, budget))
			break
		}
		if len(s) <= budget {
			chunks = append(chunks, s)
			break
		}

		cut := cutOnBoundary(s, budget)
		if cut <= 0 {
			cut = budget
		}
		chunks = append(chunks, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}

	if len(chunks) <= 1 {
		return chunks
	}
	total := len(chunks)
	for i := range chunks {
		chunks[i] = fmt.Sprintf("%s [%d/%d]", chunks[i], i+1, total)
	}
	return chunks
}
