// Package shrink centralizes fitting bot replies into the MeshCore 140-byte
// message limit. Its entire public surface is Fit: every send path calls it
// once. See README.md for the design.
package shrink

import (
	"strings"

	"meshbot/internal/config"
)

// Variant selects a mesh-fit strategy.
type Variant string

const (
	Truncate Variant = "truncate" // hard cut to fit, rune-safe, ellipsis
	Split    Variant = "split"    // split into "[i/N]" parts (≤9)
	LLM      Variant = "llm"      // LLM compress w/ retries; falls back to Split
	Skip     Variant = "skip"     // drop a message that won't fit
)

const defaultMaxBytes = 140

func maxBytes() int {
	if m := config.Get().Shrink.MaxBytes; m > 0 {
		return m
	}
	return defaultMaxBytes
}

func ellipsis() string {
	return config.Get().Shrink.Ellipsis
}

func valid(v string) bool {
	switch Variant(v) {
	case Truncate, Split, LLM, Skip:
		return true
	}
	return false
}

// builtinDefaults pins the variant for send paths whose behavior should not
// silently inherit the global default: a voice assistant reading a truncated
// reply loses its ending, so the XiaoZhi path defaults to Split.
var builtinDefaults = map[string]Variant{
	"xiaozhi": Split,
}

// resolveVariant picks the variant for name: per-name override, then the
// path's built-in default, then the global default, then Truncate as a safe
// last resort. Invalid values are skipped.
func resolveVariant(name string) Variant {
	s := config.Get().Shrink
	if v, ok := s.Overrides[name]; ok && valid(v) {
		return Variant(v)
	}
	if d, ok := builtinDefaults[name]; ok {
		return d
	}
	if valid(s.Default) {
		return Variant(s.Default)
	}
	return Truncate
}

// Fit turns a handler's logical messages into mesh-ready wire messages: it fits
// each element of msgs via the variant configured for name, flattens the result,
// and prepends prefix to the first wire message. Nothing returned exceeds the
// configured max. prefix is "" when there is none (DMs, bot-originated posts).
func Fit(name, prefix string, msgs []string) []string {
	v := resolveVariant(name)
	max := maxBytes()
	// The prefix is part of the wire bytes, so squeeze it too (visually
	// identical) and account for its post-transform length.
	prefix = maybeCyrToLat(prefix)
	head := len(prefix)

	var out []string
	for _, m := range msgs {
		parts := fitOne(v, m, max, head)
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

// fitOne fits a single logical message. head reserves bytes for a prefix on the
// first produced wire message.
func fitOne(v Variant, text string, max, head int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	budget := max - head
	if budget < 1 {
		budget = 1
	}
	// Lossless pre-transform: the squeezed text drives the fit decision and the
	// truncate/split/skip variants. The LLM variant is the exception — it gets
	// the original Cyrillic so its input is clean, and squeezes its output
	// itself (see llmShrink).
	transformed := maybeCyrToLat(text)
	if len(transformed) <= budget {
		return []string{transformed}
	}
	switch v {
	case Skip:
		return nil
	case Split:
		return split(transformed, max, head)
	case LLM:
		return llmShrink(text, max, head)
	default: // Truncate and any fallthrough
		return []string{truncate(transformed, budget)}
	}
}

// ValidateConfig logs a warning for any unknown variant in the Shrink config.
// Unknown values are ignored at runtime (they fall through to the default).
func ValidateConfig() {
	s := config.Get().Shrink
	if s.Default != "" && !valid(s.Default) {
		log().Warn("shrink: unknown default variant, using truncate", "value", s.Default)
	}
	for name, v := range s.Overrides {
		if !valid(v) {
			log().Warn("shrink: unknown override variant, ignoring", "name", name, "value", v)
		}
	}
}
