package shrink

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"meshbot/internal/config"
)

// TestMain loads a config with a Shrink section so Fit() has limits to read.
// LLM.enabled is false, so the "llm" variant deterministically falls back to
// Split (no network calls in tests).
func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "shrinkcfg")
	path := filepath.Join(dir, "config.yaml")
	yaml := `
Shrink:
  default: llm
  max_bytes: 140
  ellipsis: "…"
  overrides:
    sp: split
    tr: truncate
    sk: skip
LLM:
  enabled: false
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		panic(err)
	}
	if err := config.Load(path); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const max = 140

func longASCII(n int) string {
	var b strings.Builder
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}
	for b.Len() < n {
		b.WriteString(words[b.Len()%len(words)])
		b.WriteByte(' ')
	}
	return b.String()[:n]
}

func longCyrillic(n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString("привет мир ")
	}
	return b.String()
}

func checkAllFit(t *testing.T, msgs []string, headPrefix string) {
	t.Helper()
	for i, m := range msgs {
		if len(m) > max {
			t.Errorf("msg %d over max: %d bytes %q", i, len(m), m)
		}
		if !utf8.ValidString(m) {
			t.Errorf("msg %d not valid UTF-8: %q", i, m)
		}
	}
	if headPrefix != "" && len(msgs) > 0 && !strings.HasPrefix(msgs[0], headPrefix) {
		t.Errorf("first msg missing prefix %q: %q", headPrefix, msgs[0])
	}
}

func TestShortPassesThrough(t *testing.T) {
	out := Fit("tr", "", []string{"hello"})
	if len(out) != 1 || out[0] != "hello" {
		t.Fatalf("got %v", out)
	}
}

func TestTruncateAddsEllipsisAndFits(t *testing.T) {
	out := Fit("tr", "", []string{longASCII(400)})
	if len(out) != 1 {
		t.Fatalf("truncate should yield 1 msg, got %d", len(out))
	}
	checkAllFit(t, out, "")
	if !strings.HasSuffix(out[0], "…") {
		t.Errorf("expected ellipsis suffix: %q", out[0])
	}
}

var markerRe = regexp.MustCompile(` \[(\d+)/(\d+)\]$`)

func TestSplitMarkersAndFit(t *testing.T) {
	out := Fit("sp", "", []string{longASCII(400)})
	if len(out) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(out))
	}
	checkAllFit(t, out, "")
	total := len(out)
	for i, m := range out {
		want := fmt.Sprintf(" [%d/%d]", i+1, total)
		if !strings.HasSuffix(m, want) {
			t.Errorf("part %d missing marker %q: %q", i, want, m)
		}
	}
}

func TestSplitCapNineParts(t *testing.T) {
	out := Fit("sp", "", []string{longASCII(4000)})
	if len(out) > 9 {
		t.Fatalf("expected ≤9 parts, got %d", len(out))
	}
	if len(out) != 9 {
		t.Fatalf("4000 bytes should hit the 9-part cap, got %d", len(out))
	}
	checkAllFit(t, out, "")
}

func TestSplitRuneSafeCyrillic(t *testing.T) {
	out := Fit("sp", "", []string{longCyrillic(600)})
	checkAllFit(t, out, "") // checkAllFit validates UTF-8 of every part
	if len(out) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(out))
	}
}

func TestSkipDropsLongKeepsShort(t *testing.T) {
	if out := Fit("sk", "", []string{longASCII(400)}); out != nil {
		t.Errorf("skip should drop long msg, got %v", out)
	}
	if out := Fit("sk", "", []string{"short"}); len(out) != 1 || out[0] != "short" {
		t.Errorf("skip should keep short msg, got %v", out)
	}
}

func TestPrefixOnFirstMessageOnlyAndFits(t *testing.T) {
	prefix := "@[Пользователь] " // multi-byte sender name
	out := Fit("sp", prefix, []string{longASCII(400)})
	checkAllFit(t, out, prefix)
	for i := 1; i < len(out); i++ {
		if strings.HasPrefix(out[i], prefix) {
			t.Errorf("prefix leaked onto part %d: %q", i, out[i])
		}
	}
}

func TestListEachItemIndependentPrefixFirstOnly(t *testing.T) {
	prefix := "@[u] "
	out := Fit("tr", prefix, []string{"one", "two", "three"})
	if len(out) != 3 {
		t.Fatalf("expected 3 msgs, got %v", out)
	}
	if out[0] != prefix+"one" {
		t.Errorf("first msg wrong: %q", out[0])
	}
	if out[1] != "two" || out[2] != "three" {
		t.Errorf("non-first msgs got prefix: %v", out)
	}
}

func TestSkippedFirstItemMovesPrefixToNext(t *testing.T) {
	// First item is dropped by skip; the prefix must land on the next emitted msg.
	prefix := "P: "
	out := Fit("sk", prefix, []string{longASCII(400), "kept"})
	if len(out) != 1 || out[0] != prefix+"kept" {
		t.Fatalf("prefix should move to first emitted msg, got %v", out)
	}
}
