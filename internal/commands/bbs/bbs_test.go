package bbs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

// Regression: a threads.json containing a zero-post thread used to panic
// bbsList (Posts[0]). validThreads must drop it at load, and bbsList must
// survive one anyway.
func TestValidThreadsDropsEmpty(t *testing.T) {
	now := time.Now()
	threads := []*BBSThread{
		{ID: 1, Title: "empty", CreatedAt: now},
		{ID: 2, Title: "ok", CreatedAt: now, Posts: []BBSPost{{Num: 1}}},
	}
	kept := validThreads(threads)
	if len(kept) != 1 || kept[0].ID != 2 {
		t.Fatalf("kept = %+v, want only the thread with posts", kept)
	}
}

func TestBBSListSurvivesEmptyThread(t *testing.T) {
	saved := bbsThreads
	bbsThreads = []*BBSThread{{ID: 1, Title: "poison", CreatedAt: time.Now()}}
	defer func() { bbsThreads = saved }()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("bbsList panicked on a zero-post thread: %v", r)
		}
	}()
	out := bbsList("ru", "/ls")
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "1/1") {
		t.Errorf("unexpected list output: %q", joined)
	}
}

// Regression: sessions are keyed on SenderKey, which is "" for senders the
// bot cannot resolve. One anonymous ".2ch" used to capture every unidentified
// sender bot-wide until someone typed .exit.
func TestEmptySenderKeyCannotEnter(t *testing.T) {
	if isBBSMode("") {
		t.Fatal("precondition: empty key must not start in BBS mode")
	}
	reply, err := plugin{}.Handle(registry.NewCtx(".2ch", "chan", "", "anon", nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply[0], "Добро пожаловать") || strings.Contains(reply[0], "Welcome") {
		t.Fatalf("anonymous sender must be refused, got welcome: %q", reply[0])
	}
	if isBBSMode("") {
		t.Fatal("empty-key session must not be created")
	}
}

// A forgotten .exit must not swallow the sender forever.
func TestSessionExpiresAfterTTL(t *testing.T) {
	enterBBSMode("k1")
	if !isBBSMode("k1") {
		t.Fatal("fresh session must be active")
	}
	bbsSessionsMu.Lock()
	bbsSessions["k1"] = time.Now().Add(-bbsSessionTTL - time.Minute)
	bbsSessionsMu.Unlock()
	if isBBSMode("k1") {
		t.Fatal("stale session must be dropped")
	}
	exitBBSMode("k1") // idempotent cleanup
}

// bbsSave must write atomically and keep a .bak generation; bbsLoad must
// recover from both a missing main file and a corrupt main file.
func TestSaveLoadRoundTripAndBakFallback(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldFile := bbsDir, bbsThreadsFile
	bbsDir, bbsThreadsFile = dir, filepath.Join(dir, "threads.json")
	oldLoaded, oldThreads := bbsLoaded, bbsThreads
	defer func() {
		bbsDir, bbsThreadsFile = oldDir, oldFile
		bbsLoaded, bbsThreads = oldLoaded, oldThreads
	}()

	bbsLoaded = false
	bbsThreads = []*BBSThread{{
		ID: 7, Title: "_saved", CreatedAt: time.Now(),
		Posts: []BBSPost{{Num: 1, AuthorKey: "kk", AuthorName: "author", CreatedAt: time.Now(), Text: "body"}},
	}}
	if err := bbsSave(); err != nil {
		t.Fatalf("bbsSave: %v", err)
	}
	// Real usage always saves after each mutation, and only from the second
	// save onward does a .bak generation exist.
	if err := bbsSave(); err != nil {
		t.Fatalf("bbsSave (2nd): %v", err)
	}

	// Round-trip: a fresh load reads the same thread back.
	bbsLoaded = false
	bbsThreads = nil
	if err := bbsLoad(); err != nil {
		t.Fatalf("bbsLoad: %v", err)
	}
	if len(bbsThreads) != 1 || bbsThreads[0].Title != "_saved" {
		t.Fatalf("round-trip lost threads: %+v", bbsThreads)
	}

	// Missing main file → .bak fallback.
	os.Remove(bbsThreadsFile)
	bbsLoaded = false
	bbsThreads = nil
	if err := bbsLoad(); err != nil {
		t.Fatalf("bbsLoad from .bak: %v", err)
	}
	if len(bbsThreads) != 1 || bbsThreads[0].Title != "_saved" {
		t.Fatalf(".bak fallback lost threads: %+v", bbsThreads)
	}

	// Corrupt main file → .bak fallback.
	os.WriteFile(bbsThreadsFile, []byte("{not json"), 0o644)
	bbsLoaded = false
	bbsThreads = nil
	if err := bbsLoad(); err != nil {
		t.Fatalf("bbsLoad with corrupt file: %v", err)
	}
	if len(bbsThreads) != 1 || bbsThreads[0].Title != "_saved" {
		t.Fatalf("corrupt-file fallback lost threads: %+v", bbsThreads)
	}
}

// The forum must not grow without bound from anonymous mesh input.
func TestPruneThreadsCapsGrowth(t *testing.T) {
	old := bbsThreads
	defer func() { bbsThreads = old }()

	now := time.Now()
	bbsThreads = nil
	for i := 0; i < bbsMaxThreads+10; i++ {
		bbsThreads = append(bbsThreads, &BBSThread{
			ID: i, Title: "t", CreatedAt: now.Add(-time.Duration(i) * time.Hour),
			Posts: []BBSPost{{Num: 1, CreatedAt: now.Add(-time.Duration(i) * time.Hour)}},
		})
	}
	pruneThreads()
	if len(bbsThreads) != bbsMaxThreads {
		t.Fatalf("pruned to %d, want %d", len(bbsThreads), bbsMaxThreads)
	}
	// The oldest-activity threads are the ones dropped (newest 50 kept).
	for _, th := range bbsThreads {
		if th.ID >= 50 {
			t.Fatalf("thread %d (oldest) should have been pruned", th.ID)
		}
	}
}

// Byte-slice truncation used to emit invalid UTF-8 for Cyrillic names.
func TestBBSAuthorUTF8(t *testing.T) {
	got := bbsAuthor("aabbcc", "ОченьДлинноеИмя")
	if !utf8.ValidString(got) {
		t.Fatalf("bbsAuthor emitted invalid UTF-8: %q", got)
	}
	if got != "ОченьДли…" {
		t.Errorf("bbsAuthor = %q, want rune-clipped with ellipsis", got)
	}
}

// Sub-command matching follows the registry prefix rule: /rdzone is not /rd.
func TestSubCommandPrefixDiscipline(t *testing.T) {
	old, oldLoaded := bbsThreads, bbsLoaded
	bbsThreads, bbsLoaded = nil, true
	defer func() { bbsThreads, bbsLoaded = old, oldLoaded }()

	out, err := processBBSCommand("en", "/rdzone", "kk", "nm")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0], "Unknown") {
		t.Fatalf("/rdzone must fall through to unknown_cmd, got %q", out[0])
	}
}
