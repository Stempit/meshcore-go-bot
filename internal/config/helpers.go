package config

import "slices"

// IsAllowed returns true if the key should be processed.
// Whitelist takes priority: if non-empty, key must be in it.
// Otherwise key must not be in blacklist.
func (s *SecurityListConfig) IsAllowed(key string) bool {
	if len(s.Whitelist) > 0 {
		return slices.Contains(s.Whitelist, key)
	}
	return !slices.Contains(s.Blacklist, key)
}

// Lookup returns the redirect notice for command name on channel, and whether a
// rule matched. It returns false when redirect is disabled, channel is empty
// (DM), or no rule covers this command/channel pair.
func (r *RedirectConfig) Lookup(name, channel string) (string, bool) {
	if !r.Enabled || channel == "" {
		return "", false
	}
	for _, rule := range r.Rules {
		if !slices.Contains(rule.Commands, name) {
			continue
		}
		if len(rule.On) > 0 && !slices.Contains(rule.On, channel) {
			continue
		}
		return rule.Message, true
	}
	return "", false
}
