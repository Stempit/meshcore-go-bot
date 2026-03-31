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
