// Package historylog persists incoming and outgoing mesh messages to per-conversation
// text files under a configurable base directory.
//
// Layout:
//
//	<baseDir>/channels/<channel_name>.txt  – channel (group) messages
//	<baseDir>/dm/<contact_name>.txt        – direct (private) messages
//
// Each line is one message:
//
//	<< 2006-01-02 15:04:05 [SenderName]: message text
//	>> 2006-01-02 15:04:05 [Me]: message text
package historylog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Entry is a single parsed history record.
type Entry struct {
	Time   time.Time
	Sender string // sender name; "Me" for outgoing messages
	Text   string
}

// lineRe matches a log line: << 2006-01-02 15:04:05 [Sender]: text
var lineRe = regexp.MustCompile(`^([<>]{2}) (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) \[([^\]]+)\]: (.*)$`)

func parseLine(line string) (Entry, bool) {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return Entry{}, false
	}
	t, err := time.ParseInLocation(timeLayout, m[2], time.Local)
	if err != nil {
		return Entry{}, false
	}
	return Entry{Time: t, Sender: m[3], Text: m[4]}, true
}

// ReadLastChannel returns the last n entries from the named channel log under baseDir.
// Returns nil, nil if the log file does not exist yet.
func ReadLastChannel(baseDir, channelName string, n int) ([]Entry, error) {
	path := filepath.Join(baseDir, "channels", sanitize(channelName)+".txt")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("historylog: read %s: %w", path, err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	var entries []Entry
	for _, line := range lines {
		if e, ok := parseLine(line); ok {
			entries = append(entries, e)
		}
	}

	if n > len(entries) {
		n = len(entries)
	}
	return entries[len(entries)-n:], nil
}

const timeLayout = "2006-01-02 15:04:05"

// Logger writes conversation history to files. It is safe for concurrent use.
// File handles are kept open for the lifetime of the Logger; call Close when done.
type Logger struct {
	baseDir string

	mu    sync.Mutex
	files map[string]*os.File // key: relative path without extension
}

// New creates a Logger that writes files under baseDir.
// The directory (and any sub-directories) is created lazily on first write.
func New(baseDir string) *Logger {
	return &Logger{
		baseDir: baseDir,
		files:   make(map[string]*os.File),
	}
}

// LogChannelIncoming records a message received in a channel.
func (l *Logger) LogChannelIncoming(channelName, senderName, text string, t time.Time) error {
	line := fmt.Sprintf("<< %s [%s]: %s\n", t.Format(timeLayout), senderName, text)
	return l.write(filepath.Join("channels", sanitize(channelName)), line)
}

// LogChannelOutgoing records a message sent to a channel.
func (l *Logger) LogChannelOutgoing(channelName, text string, t time.Time) error {
	line := fmt.Sprintf(">> %s [Me]: %s\n", t.Format(timeLayout), text)
	return l.write(filepath.Join("channels", sanitize(channelName)), line)
}

// LogDMIncoming records a direct message received from a contact.
func (l *Logger) LogDMIncoming(contactName, text string, t time.Time) error {
	line := fmt.Sprintf("<< %s [%s]: %s\n", t.Format(timeLayout), contactName, text)
	return l.write(filepath.Join("dm", sanitize(contactName)), line)
}

// LogDMOutgoing records a direct message sent to a contact.
func (l *Logger) LogDMOutgoing(contactName, text string, t time.Time) error {
	line := fmt.Sprintf(">> %s [Me]: %s\n", t.Format(timeLayout), text)
	return l.write(filepath.Join("dm", sanitize(contactName)), line)
}

// Close flushes and closes all open file handles.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var lastErr error
	for _, f := range l.files {
		if err := f.Close(); err != nil {
			lastErr = err
		}
	}
	l.files = make(map[string]*os.File)
	return lastErr
}

// write appends line to the file at <baseDir>/<relKey>.txt, creating it if needed.
func (l *Logger) write(relKey, line string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, ok := l.files[relKey]
	if !ok {
		dir := filepath.Join(l.baseDir, filepath.Dir(relKey))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("historylog: create dir %s: %w", dir, err)
		}
		path := filepath.Join(l.baseDir, relKey+".txt")
		var err error
		f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("historylog: open %s: %w", path, err)
		}
		l.files[relKey] = f
	}

	if _, err := fmt.Fprint(f, line); err != nil {
		return fmt.Errorf("historylog: write: %w", err)
	}
	return nil
}

// sanitize converts a conversation name into a safe file-system component.
// Unicode letters and digits are preserved; spaces become underscores; other
// characters are dropped. An empty result falls back to "unknown".
func sanitize(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
