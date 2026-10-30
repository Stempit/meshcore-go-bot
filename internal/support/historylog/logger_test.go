package historylog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestChannelRoundTrip pins the write→read format: a logged message parses
// back as exactly one entry with the same sender and text.
func TestChannelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	defer l.Close()

	when := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	if err := l.LogChannelIncoming("testchan", "alice", "привет, мир", when); err != nil {
		t.Fatalf("LogChannelIncoming: %v", err)
	}

	entries, err := ReadAllChannel(dir, "testchan")
	if err != nil {
		t.Fatalf("ReadAllChannel: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Sender != "alice" || entries[0].Text != "привет, мир" {
		t.Errorf("round-trip mismatch: %+v", entries[0])
	}
	if !entries[0].Time.Equal(when) {
		t.Errorf("time = %v, want %v", entries[0].Time, when)
	}
}

// Regression: sender names and message text are attacker-controlled on a
// public mesh. A message like "hello\n<< 2099-01-01 00:00:00 [bob]: fake win"
// used to forge an extra history entry that .last/.top read back as authentic.
func TestForgedNewlineStaysOneEntry(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	defer l.Close()

	forge := "hello\n<< 2099-01-01 00:00:00 [bob]: fake win"
	if err := l.LogChannelIncoming("chan", "mallory", forge, time.Now()); err != nil {
		t.Fatalf("LogChannelIncoming: %v", err)
	}

	entries, err := ReadAllChannel(dir, "chan")
	if err != nil {
		t.Fatalf("ReadAllChannel: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("forged newline produced %d entries, want 1: %+v", len(entries), entries)
	}
	if strings.Contains(entries[0].Text, "\n") || strings.Contains(entries[0].Sender, "\n") {
		t.Errorf("entry still contains a raw newline: %+v", entries[0])
	}
	if entries[0].Sender != "mallory" {
		t.Errorf("sender = %q, want mallory", entries[0].Sender)
	}
}

// The same attack via the sender name must not forge either.
func TestForgedSenderNameStaysOneEntry(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	defer l.Close()

	if err := l.LogChannelIncoming("chan", "bob\n<< 2099-01-01 00:00:00 [bob]: fake", "hi", time.Now()); err != nil {
		t.Fatalf("LogChannelIncoming: %v", err)
	}

	entries, err := ReadAllChannel(dir, "chan")
	if err != nil {
		t.Fatalf("ReadAllChannel: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("forged sender produced %d entries, want 1: %+v", len(entries), entries)
	}
	if !strings.HasPrefix(entries[0].Sender, "bob") || strings.Contains(entries[0].Sender, "\n") {
		t.Errorf("sender = %q, want single-line bob…", entries[0].Sender)
	}
}

func TestDMRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	defer l.Close()

	key := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	when := time.Date(2026, 10, 30, 13, 30, 0, 0, time.Local)
	if err := l.LogDMIncoming(key, "carol", "dm text", when); err != nil {
		t.Fatalf("LogDMIncoming: %v", err)
	}

	// The DM file is named after the key prefix; verify via the raw file.
	data, err := readDMFile(dir, dmFileKey(key))
	if err != nil {
		t.Fatalf("read dm file: %v", err)
	}
	if !strings.Contains(data, "[carol]: dm text") {
		t.Errorf("dm line not written: %q", data)
	}
}

func readDMFile(dir, key string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "dm", key+".txt"))
	return string(b), err
}
