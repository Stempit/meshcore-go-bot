package claw

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Regression: with a bot name whose lowercase form is byte-shorter (İ → i),
// the old Trimmed[len(prefix):] slice panicked on the user-typed lowercase
// mention. The cut must be by rune count.
func TestMentionPrompt(t *testing.T) {
	cases := []struct {
		name    string
		trimmed string
		prefix  string // config-case
		want    string
	}{
		{"ascii name, lowercase mention", "@[yorick] hello there", "@[Yorick]", "hello there"},
		{"İ name, lowercase mention", "@[ibo] вопрос", "@[İbo]", "вопрос"},
		{"İ name, exact-case mention", "@[İbo] вопрос", "@[İbo]", "вопрос"},
		{"no space after mention", "@[İbo]вопрос", "@[İbo]", "вопрос"},
		{"empty prompt", "@[ibo]", "@[İbo]", ""},
		{"emoji name", "@[y💀] hi", "@[Y💀]", "hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mentionPrompt(tc.trimmed, tc.prefix)
			if got != tc.want {
				t.Errorf("mentionPrompt(%q, %q) = %q, want %q", tc.trimmed, tc.prefix, got, tc.want)
			}
			// The extracted prompt must round-trip as a .claw command without
			// invalid UTF-8 anywhere (byte-slice truncation produced some).
			if !utf8.ValidString(got) {
				t.Errorf("prompt is not valid UTF-8: %q", got)
			}
		})
	}
}

func TestMentionPromptDoesNotPanicOnShortInput(t *testing.T) {
	// Prefix rune count exceeds the input: must return "" not panic.
	if got := mentionPrompt("@[i", "@[İbo]"); got != "" {
		t.Errorf("want empty prompt, got %q", got)
	}
	if !strings.Contains(mentionPrompt("@[İbo] x", "@[İbo]"), "x") {
		t.Error("exact-case mention must still extract the prompt")
	}
}
