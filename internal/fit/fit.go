// Package fit shapes outgoing text into wire-ready mesh messages: it fits
// arbitrary logical replies into the MeshCore size limit (140 bytes by
// default) via truncate / split / skip variants, an optional lossless
// Cyrillic→Latin homoglyph squeeze, and an injectable LLM compressor. The
// package is pure — no config, no providers; callers resolve their policy
// into Options (see internal/shrink for the config-driven policy layer).
package fit

import (
	"strings"
)

// Variant selects a mesh-fit strategy.
type Variant string

const (
	Truncate Variant = "truncate" // hard cut to fit, rune-safe, ellipsis
	Split    Variant = "split"    // split into "[i/N]" parts (≤9)
	LLM      Variant = "llm"      // Compress compresses; falls back to Split
	Skip     Variant = "skip"     // drop a message that won't fit
)

// DefaultMaxBytes is the hard mesh limit used when Options.MaxBytes is unset.
const DefaultMaxBytes = 140

// Compressor compresses text for the LLM variant. It receives the original
// (unsqueezed) text and must return wire-ready parts that respect the budget
// implied by o and head — falling back to Split(o, text, head) itself when it
// cannot deliver a short-enough result.
type Compressor func(o Options, text string, head int) []string

// Options is the resolved fitting policy for one send path.
type Options struct {
	MaxBytes int       // hard limit; 0 → DefaultMaxBytes
	Ellipsis string    // appended by truncate (counts toward the limit)
	CyrToLat bool      // lossless Cyrillic→Latin homoglyph squeeze
	Variant  Variant   // strategy; "" behaves as Truncate
	Compress Compressor // used by the LLM variant; nil → Split fallback
}

// Max returns the effective byte limit.
func (o Options) Max() int {
	if o.MaxBytes > 0 {
		return o.MaxBytes
	}
	return DefaultMaxBytes
}

// Budget returns the per-part byte budget after reserving head bytes for a
// send-layer prefix, clamped to at least 1.
func (o Options) Budget(head int) int {
	if b := o.Max() - head; b > 0 {
		return b
	}
	return 1
}

// Squeeze applies the lossless Cyrillic→Latin transform when enabled;
// otherwise it returns s unchanged.
func (o Options) Squeeze(s string) string {
	if o.CyrToLat {
		return CyrToLat(s)
	}
	return s
}

// Fit turns logical messages into mesh-ready wire messages: it fits each
// element of msgs via the configured variant, flattens the result, and
// prepends prefix to the first wire message. Nothing returned exceeds
// Options.Max. prefix is "" when there is none (DMs, bot-originated posts).
func Fit(o Options, prefix string, msgs []string) []string {
	max := o.Max()
	// The prefix is part of the wire bytes, so squeeze it too (visually
	// identical) and account for its post-transform length.
	prefix = o.Squeeze(prefix)
	head := len(prefix)

	var out []string
	for _, m := range msgs {
		parts := fitOne(o, m, head)
		if len(parts) > 0 {
			// The prefix lands on the first emitted wire message only;
			// subsequent messages get the full budget.
			head = 0
		}
		out = append(out, parts...)
	}

	if prefix != "" && len(out) > 0 {
		out[0] = prefix + out[0]
	}
	for i := range out {
		out[i] = hardCap(out[i], max)
	}
	return out
}

// fitOne fits a single logical message. head reserves bytes for a prefix on
// the first produced wire message.
func fitOne(o Options, text string, head int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	budget := o.Budget(head)
	// Lossless pre-transform: the squeezed text drives the fit decision and
	// the truncate/split/skip variants. The LLM variant is the exception — it
	// gets the original Cyrillic so its input is clean, and squeezes its
	// output itself (see the injected Compressor).
	transformed := o.Squeeze(text)
	if len(transformed) <= budget {
		return []string{transformed}
	}
	switch o.Variant {
	case Skip:
		return nil
	case Split:
		return split(transformed, o.Max(), head, o.Ellipsis)
	case LLM:
		if o.Compress != nil {
			return o.Compress(o, text, head)
		}
		return split(transformed, o.Max(), head, o.Ellipsis)
	default: // Truncate and any fallthrough
		return []string{truncate(transformed, budget, o.Ellipsis)}
	}
}
