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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

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
