package registry

import (
	"strings"
	"testing"

	"github.com/stempit/meshcore-go-bot/internal/config"
)

// restEcho is a fake command whose Handle returns Ctx.Rest verbatim, so
// Dispatch-level tests can observe exactly what the handler receives.
type restEcho struct {
	name     string
	triggers []string
	match    MatchMode
}

func (f restEcho) Meta() Meta {
	return Meta{Name: f.name, Triggers: f.triggers, Match: f.match}
}
func (f restEcho) Handle(c Ctx) ([]string, error) {
	return []string{"rest=[" + c.Rest + "]"}, nil
}

func registerEcho(t *testing.T, f restEcho) {
	t.Helper()
	Register(f)
	t.Cleanup(func() {
		for i, c := range registered {
			if c.Meta().Name == f.name {
				registered = append(registered[:i], registered[i+1:]...)
				return
			}
		}
	})
}

func cfgForDispatch() *config.Config { return &config.Config{} }

// Regression for the systemic case-sensitivity bug: dispatch matches triggers
// on the lowercased text, but plugins used to strip the trigger from the raw
// text case-sensitively, so ".DICE 2d6" produced a no-op strip and garbage
// args. Dispatch must fill Rest with a case-insensitive cut.
func TestDispatchFillsRestCaseInsensitively(t *testing.T) {
	registerEcho(t, restEcho{name: "echo", triggers: []string{".echo"}, match: Prefix})

	cases := []struct {
		in   string
		want string
	}{
		{".echo hello world", "hello world"},
		{".ECHO hello world", "hello world"},
		{".Echo   spaced   ", "spaced"},
		{".echo", ""},
		{".echo ", ""},
	}
	for _, tc := range cases {
		replies, _, err := Dispatch(NewCtx(tc.in, "", "k", "n", nil, cfgForDispatch()))
		if err != nil {
			t.Fatalf("Dispatch(%q): %v", tc.in, err)
		}
		if len(replies) != 1 {
			t.Fatalf("Dispatch(%q) = %v, want one reply", tc.in, replies)
		}
		if got := strings.TrimPrefix(replies[0], "rest=["); got == replies[0] || strings.TrimSuffix(got, "]") != tc.want {
			t.Errorf("Dispatch(%q) Rest = %q, want %q", tc.in, replies[0], tc.want)
		}
	}
}

func TestDispatchExactMatchLeavesRestEmpty(t *testing.T) {
	registerEcho(t, restEcho{name: "exact", triggers: []string{".exact"}, match: Exact})

	replies, _, err := Dispatch(NewCtx(".exact", "", "k", "n", nil, cfgForDispatch()))
	if err != nil {
		t.Fatal(err)
	}
	if replies[0] != "rest=[]" {
		t.Errorf("exact match Rest = %q, want empty", replies[0])
	}
	// ".exact extra" must not match an Exact trigger at all.
	replies, name, err := Dispatch(NewCtx(".exact extra", "", "k", "n", nil, cfgForDispatch()))
	if err != nil || replies != nil || name != "" {
		t.Errorf("exact trigger must not match trailing text, got %v %q %v", replies, name, err)
	}
}

// Arg cuts case-insensitively and never falls back to the whole text.
func TestArgCaseInsensitive(t *testing.T) {
	cases := []struct {
		text, prefix, want string
	}{
		{".w moscow", ".w", "moscow"},
		{".W moscow", ".w", "moscow"},
		{".w  moscow ", ".w", "moscow"},
		{".w", ".w", ""},
		{".w\tmoscow", ".w", ""}, // registry Prefix rule: separator is a space
		{".words x", ".w", ""},   // prefix must not slice mid-word
		{".2ch", ".2ch", ""},
		{".2Ch body", ".2ch", "body"},
	}
	for _, tc := range cases {
		c := NewCtx(tc.text, "", "k", "n", nil, nil)
		if got := c.Arg(tc.prefix); got != tc.want {
			t.Errorf("Arg(%q, %q) = %q, want %q", tc.text, tc.prefix, got, tc.want)
		}
	}
}
