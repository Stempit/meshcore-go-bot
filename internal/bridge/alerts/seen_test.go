package alerts

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression: CheckAndAdd used to record the id in memory BEFORE writing it
// to disk, so one transient write error permanently swallowed the event —
// every later poll saw it as known — while it stayed missing from disk.
func TestCheckAndAddRetriesAfterWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.txt")
	s, err := NewSeenSet(path)
	if err != nil {
		t.Fatal(err)
	}

	known, err := s.CheckAndAdd("https://example.com/a")
	if err != nil || known {
		t.Fatalf("first add: known=%v err=%v", known, err)
	}
	if known, _ := s.CheckAndAdd("https://example.com/a"); !known {
		t.Fatal("second add must report already-known")
	}

	// Make the write fail: strip write permission from the file.
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	known, err = s.CheckAndAdd("https://example.com/b")
	if err == nil {
		t.Log("write unexpectedly succeeded (running as root?); retry semantics hold trivially")
		return
	}

	// The failed id must NOT be recorded: restoring permissions lets it through.
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	known, err = s.CheckAndAdd("https://example.com/b")
	if err != nil || known {
		t.Fatalf("failed write must not swallow the id: known=%v err=%v", known, err)
	}
}

// The seen set must survive a restart: reload from disk and report known ids.
func TestSeenSetPersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.txt")
	s, err := NewSeenSet(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckAndAdd("id-1"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewSeenSet(path)
	if err != nil {
		t.Fatal(err)
	}
	if known, err := reloaded.CheckAndAdd("id-1"); err != nil || !known {
		t.Fatalf("reload lost the id: known=%v err=%v", known, err)
	}
}
