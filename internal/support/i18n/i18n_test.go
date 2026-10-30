package i18n

import (
	"strings"
	"testing"
	"testing/fstest"
)

// T formats via fmt.Sprintf, so translations must keep the same number of
// %-verbs as the base language; Load rejects mismatched catalogs at startup
// instead of letting a garbled reply ship over the radio.
func TestLoadRejectsVerbMismatch(t *testing.T) {
	fsys := fstest.MapFS{
		"en.yaml": &fstest.MapFile{Data: []byte("greet: \"Hello %s, you have %d points\"\nplain: \"hi\"\n")},
		"ru.yaml": &fstest.MapFile{Data: []byte("greet: \"Привет %s\"\nplain: \"привет\"\n")},
	}
	_, err := Load(fsys)
	if err == nil {
		t.Fatal("Load must reject a translation with a different placeholder count")
	}
	if !strings.Contains(err.Error(), "greet") || !strings.Contains(err.Error(), "ru") {
		t.Errorf("error should name the key and language: %v", err)
	}
}

func TestLoadAcceptsMatchingCatalogs(t *testing.T) {
	fsys := fstest.MapFS{
		"en.yaml": &fstest.MapFile{Data: []byte("greet: \"Hello %s, you have %d points\"\nplain: \"done %d%%\"\n")},
		"ru.yaml": &fstest.MapFile{Data: []byte("greet: \"Привет %s, у тебя %d очков\"\nplain: \"готово %d%%\"\n")},
	}
	l, err := Load(fsys)
	if err != nil {
		t.Fatalf("matching catalogs must load: %v", err)
	}
	// %% renders as a literal percent; the placeholder survives Sprintf.
	if got := l.T("ru", "greet", "Мир", 5); got != "Привет Мир, у тебя 5 очков" {
		t.Errorf("T = %q", got)
	}
	if got := l.T("en", "plain", 50); got != "done 50%" {
		t.Errorf("escaped percent: T = %q", got)
	}
}

func TestTFallsBackToBase(t *testing.T) {
	fsys := fstest.MapFS{
		"en.yaml": &fstest.MapFile{Data: []byte("only_en: \"english %s\"\n")},
		"ru.yaml": &fstest.MapFile{Data: []byte("unrelated: \"x\"\n")},
	}
	l, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.T("ru", "only_en", "a"); got != "english a" {
		t.Errorf("missing ru key must fall back to en: %q", got)
	}
	if got := l.T("ru", "absent"); got != "absent" {
		t.Errorf("absent key must surface the key: %q", got)
	}
}

func TestVerbCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"no verbs", 0},
		{"%s and %d", 2},
		{"100%% sure", 0},
		{"%64.2f", 1},
	}
	for _, tc := range cases {
		if got := verbCount(tc.in); got != tc.want {
			t.Errorf("verbCount(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
