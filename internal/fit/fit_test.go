package fit

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

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

func opts(v Variant) Options {
	return Options{MaxBytes: max, Ellipsis: "…", Variant: v}
}

func TestShortPassesThrough(t *testing.T) {
	out := Fit(opts(Truncate), "", []string{"hello"})
	if len(out) != 1 || out[0] != "hello" {
		t.Fatalf("got %v", out)
	}
}

func TestTruncateAddsEllipsisAndFits(t *testing.T) {
	out := Fit(opts(Truncate), "", []string{longASCII(400)})
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
	out := Fit(opts(Split), "", []string{longASCII(400)})
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
	out := Fit(opts(Split), "", []string{longASCII(4000)})
	if len(out) > 9 {
		t.Fatalf("expected ≤9 parts, got %d", len(out))
	}
	if len(out) != 9 {
		t.Fatalf("4000 bytes should hit the 9-part cap, got %d", len(out))
	}
	checkAllFit(t, out, "")
}

func TestSplitRuneSafeCyrillic(t *testing.T) {
	out := Fit(opts(Split), "", []string{longCyrillic(600)})
	checkAllFit(t, out, "") // checkAllFit validates UTF-8 of every part
	if len(out) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(out))
	}
}

func TestSkipDropsLongKeepsShort(t *testing.T) {
	if out := Fit(opts(Skip), "", []string{longASCII(400)}); out != nil {
		t.Errorf("skip should drop long msg, got %v", out)
	}
	if out := Fit(opts(Skip), "", []string{"short"}); len(out) != 1 || out[0] != "short" {
		t.Errorf("skip should keep short msg, got %v", out)
	}
}

func TestPrefixOnFirstMessageOnlyAndFits(t *testing.T) {
	prefix := "@[Пользователь] " // multi-byte sender name
	out := Fit(opts(Split), prefix, []string{longASCII(400)})
	checkAllFit(t, out, prefix)
	for i := 1; i < len(out); i++ {
		if strings.HasPrefix(out[i], prefix) {
			t.Errorf("prefix leaked onto part %d: %q", i, out[i])
		}
	}
}

func TestListEachItemIndependentPrefixFirstOnly(t *testing.T) {
	prefix := "@[u] "
	out := Fit(opts(Truncate), prefix, []string{"one", "two", "three"})
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
	out := Fit(opts(Skip), prefix, []string{longASCII(400), "kept"})
	if len(out) != 1 || out[0] != prefix+"kept" {
		t.Fatalf("prefix should move to first emitted msg, got %v", out)
	}
}

func TestLLMVariantWithoutCompressorFallsBackToSplit(t *testing.T) {
	out := Fit(opts(LLM), "", []string{longASCII(400)})
	if len(out) < 2 {
		t.Fatalf("nil compressor should fall back to split, got %d parts", len(out))
	}
	checkAllFit(t, out, "")
}

func TestCompressorReceivesOriginalTextAndRespectsBudget(t *testing.T) {
	var gotText string
	var gotHead int
	o := opts(LLM)
	o.Compress = func(co Options, text string, head int) []string {
		gotText, gotHead = text, head
		return []string{strings.Repeat("x", co.Budget(head))}
	}
	long := longASCII(400)
	out := Fit(o, "", []string{long})
	if gotText != strings.TrimSpace(long) {
		t.Errorf("compressor should get the original text, got %q", gotText)
	}
	if gotHead != 0 {
		t.Errorf("head = %d, want 0", gotHead)
	}
	if len(out) != 1 || len(out[0]) > max {
		t.Errorf("unexpected output: %v", out)
	}
}

func TestCyrToLatMappingAndByteSavings(t *testing.T) {
	// All-mapped letters: "соре" → "cope", 4 Cyrillic (8 B) → 4 ASCII (4 B).
	in := "соре"
	got := CyrToLat(in)
	if got != "cope" {
		t.Fatalf("CyrToLat(%q) = %q, want %q", in, got, "cope")
	}
	if saved := len(in) - len(got); saved != 4 {
		t.Errorf("expected 4 bytes saved, got %d", saved)
	}
}

func TestCyrToLatLeavesNonMappedRunes(t *testing.T) {
	// ж and lowercase к/м/т are not in the conservative set; keep them as-is.
	in := "жкмт"
	got := CyrToLat(in)
	if got != in {
		t.Errorf("non-mapped runes changed: %q -> %q", in, got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("output not valid UTF-8: %q", got)
	}
}

func TestCyrToLatDisabledLeavesOriginal(t *testing.T) {
	out := Fit(opts(Truncate), "", []string{"соре"})
	if len(out) != 1 || out[0] != "соре" {
		t.Errorf("disabled transform should keep original, got %v", out)
	}
}

func TestCyrToLatAvoidsSplit(t *testing.T) {
	// A Cyrillic message just over the limit that fits once squeezed must come
	// back as a single part with no [i/N] marker.
	text := longCyrillic(150) // > 140 bytes, all mapped letters
	if len(text) <= max {
		t.Fatalf("test setup: text should exceed max, got %d", len(text))
	}
	o := opts(Split)
	o.CyrToLat = true
	out := Fit(o, "", []string{text})
	checkAllFit(t, out, "")
	if len(out) != 1 {
		t.Fatalf("expected 1 part after squeeze, got %d: %v", len(out), out)
	}
	if markerRe.MatchString(out[0]) {
		t.Errorf("unexpected split marker: %q", out[0])
	}
}

func TestCyrToLatPrefixSqueezedAndFits(t *testing.T) {
	prefix := "@[Сорес] " // mapped Cyrillic in the prefix
	o := opts(Split)
	o.CyrToLat = true
	out := Fit(o, prefix, []string{longASCII(400)})
	// Prefix is transliterated, so check against the squeezed form.
	checkAllFit(t, out, CyrToLat(prefix))
}
