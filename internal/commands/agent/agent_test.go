package agent

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
			// The extracted prompt must round-trip as an .agent command without
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

func TestAgentSessionID(t *testing.T) {
	// senderKey is raw public-key bytes; the ID must be hex so JSON encoding
	// can't corrupt non-UTF-8 sequences, and scopes must not collide.
	cases := []struct {
		name      string
		senderKey string
		channel   string
		want      string
	}{
		{"channel scope", "\x01\x02\x03", "general", "mesh-ch-67656e6572616c"},
		{"DM scope, key trimmed to 8 bytes", "\x01\x02\x03\x04\x05\x06\x07\x08\x09", "", "mesh-dm-0102030405060708"},
		{"DM scope, short key", "abc", "", "mesh-dm-default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentSessionID(tc.senderKey, tc.channel); got != tc.want {
				t.Errorf("agentSessionID(%q, %q) = %q, want %q", tc.senderKey, tc.channel, got, tc.want)
			}
		})
	}
}
