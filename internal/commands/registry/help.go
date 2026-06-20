package registry

import "strings"

// HelpIndex renders the one-line command index from the registered Meta. It
// lists Triggers[0] for every enabled command that sets HelpShort, in
// registration order, followed by the usage hint.
func HelpIndex() string {
	var triggers []string
	for _, c := range registered {
		m := c.Meta()
		if m.HelpShort == "" || len(m.Triggers) == 0 {
			continue
		}
		if !enabled(c) {
			continue
		}
		triggers = append(triggers, m.Triggers[0])
	}
	return strings.Join(triggers, " ") + "\n\".help <cmd>\" for details"
}

// HelpFor returns the long help for the command identified by arg, which may be
// a command Name or a trigger without its leading dot ("w", "2ch", "8ball").
// Falls back to HelpIndex when nothing matches.
func HelpFor(arg string) string {
	arg = strings.ToLower(strings.TrimSpace(arg))
	for _, c := range registered {
		m := c.Meta()
		if m.HelpLong == "" || !enabled(c) {
			continue
		}
		if strings.EqualFold(m.Name, arg) {
			return m.HelpLong
		}
		for _, t := range m.Triggers {
			if strings.TrimPrefix(t, ".") == arg {
				return m.HelpLong
			}
		}
	}
	return HelpIndex()
}
