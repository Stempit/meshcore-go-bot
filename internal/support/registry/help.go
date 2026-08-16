package registry

import (
	"embed"
	"sort"
	"strings"

	"meshbot/internal/support/i18n"
)

//go:embed lang/*.yaml
var langFS embed.FS

// tr localizes the help-index group labels and footer. The Group *keys* stay
// English (Meta().Group); only their displayed labels are translated.
var tr = i18n.MustLoad(langFS)

// helpGroupOrder is the display order of .help index categories. Groups not
// listed here are appended afterward, sorted alphabetically.
var helpGroupOrder = []string{"Games", "Info", "AI", "Service", "Other"}

// HelpIndex renders the grouped command index from the registered Meta. For
// every enabled command that sets both HelpShort and Group it lists Triggers[0]
// under its group (one line per group), in registration order within a group,
// followed by the usage hint. Group labels and the footer are shown in lang.
func HelpIndex(lang string) string {
	byGroup := map[string][]string{}
	var seen []string
	for _, c := range registered {
		m := c.Meta()
		if m.HelpShort == "" || m.Group == "" || len(m.Triggers) == 0 {
			continue
		}
		if !enabled(c) {
			continue
		}
		if _, ok := byGroup[m.Group]; !ok {
			seen = append(seen, m.Group)
		}
		byGroup[m.Group] = append(byGroup[m.Group], m.Triggers[0])
	}

	var lines []string
	for _, g := range groupOrder(seen) {
		label := tr.T(lang, "group."+g)
		if label == "group."+g { // key absent from every catalog → use the raw group name
			label = g
		}
		lines = append(lines, label+": "+strings.Join(byGroup[g], " "))
	}
	lines = append(lines, tr.T(lang, "help.footer"))
	return strings.Join(lines, "\n")
}

// groupOrder returns the groups in helpGroupOrder first (only those present in
// seen), then any remaining groups sorted alphabetically.
func groupOrder(seen []string) []string {
	present := map[string]bool{}
	for _, g := range seen {
		present[g] = true
	}
	var out []string
	for _, g := range helpGroupOrder {
		if present[g] {
			out = append(out, g)
			present[g] = false
		}
	}
	var rest []string
	for g, ok := range present {
		if ok {
			rest = append(rest, g)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// helpReply implements per-command help: when ctx is exactly "<trigger> help"
// for an enabled command with a non-empty HelpLong, it returns that HelpLong and
// the command Name. The exact-match guard means "help" elsewhere is ignored, and
// commands without HelpLong fall through to normal dispatch.
func helpReply(ctx Ctx) (help, name string, ok bool) {
	for _, c := range registered {
		m := c.Meta()
		if m.HelpLong == "" || !enabled(c) {
			continue
		}
		for _, t := range m.Triggers {
			if ctx.Lower == strings.ToLower(t)+" help" {
				long := m.HelpLong
				if lz, ok := c.(Localized); ok {
					if s := lz.LocalizedHelp(ctx.Lang); s != "" {
						long = s
					}
				}
				return long, m.Name, true
			}
		}
	}
	return "", "", false
}
