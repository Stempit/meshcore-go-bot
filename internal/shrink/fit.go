// Package shrink centralizes fitting bot replies into the MeshCore 140-byte
// message limit. It is the config-driven policy layer over internal/fit:
// variants, limits and the lossless squeeze come from the Shrink config
// section (with per-send-path overrides), and the "llm" variant compresses
// via the configured provider (internal/bridge/llm) before falling back to
// fit's splitter. The pure fitting algorithms live in internal/fit.
package shrink

import (
	"meshbot/internal/config"
	"github.com/stempit/meshcore-companion/fit"
)

func valid(v string) bool {
	switch fit.Variant(v) {
	case fit.Truncate, fit.Split, fit.LLM, fit.Skip:
		return true
	}
	return false
}

// builtinDefaults pins the variant for send paths whose behavior should not
// silently inherit the global default: a voice assistant reading a truncated
// reply loses its ending, so the XiaoZhi path defaults to Split.
var builtinDefaults = map[string]fit.Variant{
	"xiaozhi": fit.Split,
}

// resolveVariant picks the variant for name: per-name override, then the
// path's built-in default, then the global default, then Truncate as a safe
// last resort. Invalid values are skipped.
func resolveVariant(name string) fit.Variant {
	s := config.Get().Shrink
	if v, ok := s.Overrides[name]; ok && valid(v) {
		return fit.Variant(v)
	}
	if d, ok := builtinDefaults[name]; ok {
		return d
	}
	if valid(s.Default) {
		return fit.Variant(s.Default)
	}
	return fit.Truncate
}

// OptionsFor resolves the fit options for a named send path from the Shrink
// config section, variant overrides included. The "xiaozhi" path gets the
// Split built-in default when nothing is configured for it.
func OptionsFor(name string) fit.Options {
	s := config.Get().Shrink
	o := fit.Options{
		MaxBytes: s.MaxBytes,
		Ellipsis: s.Ellipsis,
		CyrToLat: s.CyrToLat,
		Variant:  resolveVariant(name),
	}
	if o.Variant == fit.LLM {
		o.Compress = llmCompressor
	}
	return o
}

// Fit turns a handler's logical messages into mesh-ready wire messages: it fits
// each element of msgs via the variant configured for name, flattens the result,
// and prepends prefix to the first wire message. Nothing returned exceeds the
// configured max. prefix is "" when there is none (DMs, bot-originated posts).
func Fit(name, prefix string, msgs []string) []string {
	return fit.Fit(OptionsFor(name), prefix, msgs)
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
