package config

// IsAllowed returns true if the key should be processed.
// Whitelist takes priority: if non-empty, key must be in it.
// Otherwise key must not be in blacklist.
func (s *SecurityListConfig) IsAllowed(key string) bool {
	if len(s.Whitelist) > 0 {
		for _, v := range s.Whitelist {
			if v == key {
				return true
			}
		}
		return false
	}
	for _, v := range s.Blacklist {
		if v == key {
			return false
		}
	}
	return true
}

// Lookup returns the redirect notice for command name on channel, and whether a
// rule matched. It returns false when redirect is disabled, channel is empty
// (DM), or no rule covers this command/channel pair.
func (r *RedirectConfig) Lookup(name, channel string) (string, bool) {
	if !r.Enabled || channel == "" {
		return "", false
	}
	for _, rule := range r.Rules {
		if !contains(rule.Commands, name) {
			continue
		}
		if len(rule.On) > 0 && !contains(rule.On, channel) {
			continue
		}
		return rule.Message, true
	}
	return "", false
}

func contains(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
