// Package i18n is meshbot's tiny message-catalog layer. Each command (and the
// registry) embeds its own lang/<code>.yaml files and builds a Localizer over
// them; handlers then resolve strings by key for the language on Ctx.Lang.
//
// It depends on nothing else in meshbot (only the stdlib + yaml), so any package
// can use it without risking an import cycle. Values are plain data: a key maps
// to either a string (T) or a list of strings (TList, for random pools).
package i18n

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// Base is the fallback language code. It is the canonical key language: every
// key is expected to exist here, and lookups for any other language fall back
// to Base (then to the key literal) so a missing translation never panics.
const Base = "en"

// Localizer holds parsed catalogs keyed by language code, then by message key.
type Localizer struct {
	m map[string]map[string]any // lang -> key -> value (string or []any)
}

// Load parses every "<lang>.yaml" file found under fsys (searched recursively),
// using each file's base name without extension as the language code. Each file
// must be a flat YAML map of key -> string | list-of-strings.
func Load(fsys fs.FS) (*Localizer, error) {
	l := &Localizer{m: map[string]map[string]any{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		var cat map[string]any
		if err := yaml.Unmarshal(data, &cat); err != nil {
			return fmt.Errorf("parsing %s: %w", p, err)
		}
		lang := strings.TrimSuffix(path.Base(p), ".yaml")
		l.m[lang] = cat
		return nil
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

// MustLoad is Load for package-init use with embedded data; it panics on error.
func MustLoad(fsys fs.FS) *Localizer {
	l, err := Load(fsys)
	if err != nil {
		panic("i18n: " + err.Error())
	}
	return l
}

// lookup resolves key for lang, falling back to Base. Returns nil if absent in both.
func (l *Localizer) lookup(lang, key string) any {
	if cat, ok := l.m[lang]; ok {
		if v, ok := cat[key]; ok {
			return v
		}
	}
	if lang != Base {
		if cat, ok := l.m[Base]; ok {
			if v, ok := cat[key]; ok {
				return v
			}
		}
	}
	return nil
}

// T returns the string template for key in lang (lang -> Base -> key literal),
// formatted with args via fmt.Sprintf. Placeholders are positional (%s/%d), so
// translations must preserve argument order.
func (l *Localizer) T(lang, key string, args ...any) string {
	s, ok := l.lookup(lang, key).(string)
	if !ok {
		return key // missing everywhere: surface the key rather than crash
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// TList returns the []string value for key in lang (lang -> Base). A missing or
// non-list value yields a one-element slice holding the key, so callers that
// index into it never panic.
func (l *Localizer) TList(lang, key string) []string {
	v := l.lookup(lang, key)
	switch xs := v.(type) {
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			out = append(out, fmt.Sprint(x))
		}
		if len(out) > 0 {
			return out
		}
	case []string:
		if len(xs) > 0 {
			return xs
		}
	}
	return []string{key}
}
