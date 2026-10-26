// Package registry is the self-registering command framework. Individual
// command plugins depend on it; it never depends on them, which breaks the
// import cycle. Each plugin calls Register from its init(); dispatch and main
// only reference Ctx, Dispatch, ConfigureAll, and the Help helpers.
package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stempit/meshcore-go-bot/internal/config"
)

// MatchMode selects how a command's Triggers are compared against Ctx.Lower.
type MatchMode int

const (
	// Exact matches only when Ctx.Lower equals a trigger verbatim.
	Exact MatchMode = iota
	// Prefix matches "exact trigger OR trigger followed by a space":
	//   ctx.Lower == t || strings.HasPrefix(ctx.Lower, t+" ")
	// The trailing space is load-bearing so e.g. ".w" cannot swallow ".words".
	Prefix
)

// Meta is the declarative description every command exposes.
type Meta struct {
	Name      string    // "ping" — also the shrink.Fit variant key
	Triggers  []string  // ".ping", "ping", "пинг"; Triggers[0] is shown in the help index
	Match     MatchMode // Exact | Prefix (applies to all Triggers)
	HelpShort string    // non-empty → listed in the .help index
	HelpLong  string    // shown by ".<cmd> help"
	Group     string    // .help index category (e.g. "Games"); empty → not grouped/listed
	Priority  int       // higher runs first; interceptors use this
}

// Command is the interface every plugin implements.
type Command interface {
	Meta() Meta
	Handle(Ctx) ([]string, error)
}

// Matcher is an optional interface for custom matching beyond Triggers/Match.
// When implemented it is used INSTEAD of Triggers.
type Matcher interface {
	Matches(Ctx) bool
}

// Localized is an optional interface for a command that translates its own long
// help (".<cmd> help") per language. A non-empty return overrides Meta().HelpLong;
// an empty return (or not implementing this at all) falls back to the static
// Meta().HelpLong, so un-migrated commands keep working unchanged.
type Localized interface {
	LocalizedHelp(lang string) string
}

// Configurable is an optional interface for a per-plugin config section and an
// enable gate. Disabled plugins are invisible to both routing and help.
type Configurable interface {
	ConfigKey() string                // e.g. "words"
	Configure(config.Sectioner) error // decode + defaults + validate
	Enabled() bool                    // false hides from routing AND help
}

var registered []Command

// Register adds a command; called from each plugin's init().
func Register(c Command) { registered = append(registered, c) }

// ConfigureAll runs each Configurable plugin's Configure in registration order.
// It fails loud on a malformed/invalid section value.
func ConfigureAll(s config.Sectioner) error {
	for _, c := range registered {
		cf, ok := c.(Configurable)
		if !ok {
			continue
		}
		if err := cf.Configure(s); err != nil {
			return fmt.Errorf("configuring %q: %w", cf.ConfigKey(), err)
		}
	}
	return nil
}

// enabled reports whether a command participates in routing/help.
func enabled(c Command) bool {
	if cf, ok := c.(Configurable); ok {
		return cf.Enabled()
	}
	return true
}

// dispatchOrder returns the registered commands sorted by Priority desc, ties
// broken by registration order (stable sort over a copy).
func dispatchOrder() []Command {
	out := make([]Command, len(registered))
	copy(out, registered)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Meta().Priority > out[j].Meta().Priority
	})
	return out
}

// matches reports whether c handles ctx, via its Matcher or its Triggers/Match.
func matches(c Command, ctx Ctx) bool {
	if m, ok := c.(Matcher); ok {
		return m.Matches(ctx)
	}
	meta := c.Meta()
	for _, t := range meta.Triggers {
		switch meta.Match {
		case Exact:
			if ctx.Lower == t {
				return true
			}
		case Prefix:
			if ctx.Lower == t || strings.HasPrefix(ctx.Lower, t+" ") {
				return true
			}
		}
	}
	return false
}

// Dispatch routes ctx to the first matching enabled command (highest Priority
// first), returning its replies and Meta().Name. No match → nil, "", nil.
func Dispatch(ctx Ctx) (replies []string, name string, err error) {
	// Per-command help: "<trigger> help" returns that command's HelpLong. Runs
	// before the dispatch loop so it wins over the claw/bbs Matcher interceptors.
	if h, n, ok := helpReply(ctx); ok {
		return []string{h}, n, nil
	}
	for _, c := range dispatchOrder() {
		if !enabled(c) {
			continue
		}
		if matches(c, ctx) {
			name := c.Meta().Name
			// Redirect gate: steer flood-prone commands off busy channels before
			// the handler runs, so no game state mutates and no network call fires.
			// DMs are exempt (Lookup returns false for an empty channel).
			if msg, ok := ctx.Cfg.Redirect.Lookup(name, ctx.Channel); ok {
				if allowNotice(ctx.Channel, name, ctx.Cfg.Redirect.Cooldown) {
					return []string{msg}, name, nil
				}
				return nil, "", nil // throttled → stay silent
			}
			r, e := c.Handle(ctx)
			return r, name, e
		}
	}
	return nil, "", nil
}
