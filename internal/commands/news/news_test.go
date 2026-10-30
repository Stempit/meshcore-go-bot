package news

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFeed(t *testing.T, dir, name string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Regression: .news <feed> <count> took any count, so one message could make
// the transmitter read a whole feed file for tens of minutes. Counts are
// clamped to newsMaxCount.
func TestReadNewsClampsCount(t *testing.T) {
	dir := t.TempDir()
	old := newsDir
	newsDir = dir
	defer func() { newsDir = old }()

	lines := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		lines = append(lines, "item")
	}
	writeFeed(t, dir, "feed", lines)

	out, err := readNews("en", "feed", 100000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != newsMaxCount {
		t.Fatalf("count = %d, want clamped to %d", len(out), newsMaxCount)
	}
}

// Regression: the feed name was joined into a path unvalidated, so
// ".news ../config" read log/config.txt (arbitrary .txt reads).
func TestReadNewsRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	old := newsDir
	newsDir = dir
	defer func() { newsDir = old }()

	// A file outside the news dir that traversal would reach.
	writeFeed(t, filepath.Dir(dir), filepath.Base(dir)+"-target", []string{"secret"})

	for _, name := range []string{"../config", "..\\config", "a/b", "a b", "", ".hidden"} {
		out, err := readNews("en", name, 5, 0)
		if err != nil {
			t.Fatalf("readNews(%q): %v", name, err)
		}
		if !strings.Contains(out[0], "not found") && !strings.Contains(out[0], "Not found") {
			t.Errorf("readNews(%q) must be refused, got %q", name, out[0])
		}
	}
}

func TestReadNewsValidNamesStillWork(t *testing.T) {
	dir := t.TempDir()
	old := newsDir
	newsDir = dir
	defer func() { newsDir = old }()

	writeFeed(t, dir, "rss-world_2024", []string{"newest", "older"})
	out, err := readNews("en", "rss-world_2024", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0] != "older" || out[1] != "newest" {
		t.Fatalf("out = %v, want newest-last order", out)
	}
}

func TestFeedNameRe(t *testing.T) {
	valid := []string{"rss", "rss-world", "world_2024", "a-b_C"}
	invalid := []string{"", "../x", "a/b", "a b", ".hidden", "a\\b"}
	for _, n := range valid {
		if !feedNameRe.MatchString(n) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range invalid {
		if feedNameRe.MatchString(n) {
			t.Errorf("%q should be invalid", n)
		}
	}
}
