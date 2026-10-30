// Package historylog persists incoming and outgoing mesh messages to per-conversation
// text files under a configurable base directory.
//
// Layout:
//
//	<baseDir>/channels/<channel_name>.txt  – channel (group) messages
//	<baseDir>/dm/<key_prefix>.txt          – direct (private) messages
//
// Direct-message files are named after the contact's public-key prefix (hex) so
// that both incoming and outgoing messages for the same contact land in one file
// regardless of whether the contact's name was resolved at the time.
//
// Each line is one message:
//
//	<< 2006-01-02 15:04:05 [SenderName]: message text
//	>> 2006-01-02 15:04:05 [Me]: message text
package historylog

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/stempit/meshcore-go-bot/internal/support/helpers"
)

// keyPrefixLen is the number of public-key bytes used to name a DM log file.
// It matches the prefix length used for contact lookup elsewhere in the bot.
const keyPrefixLen = 6

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

// ReadAllChannel returns every parsed entry from the named channel log under
// baseDir, oldest first. Returns nil, nil if the log file does not exist yet.
func ReadAllChannel(baseDir, channelName string) ([]Entry, error) {
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
	return entries, nil
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

// LogDMIncoming records a direct message received from a contact. The file is
// named after contactKey's prefix; senderName is used only for display and falls
// back to the key prefix when empty (e.g. an unresolved contact).
func (l *Logger) LogDMIncoming(contactKey []byte, senderName, text string, t time.Time) error {
	key := dmFileKey(contactKey)
	if strings.TrimSpace(senderName) == "" {
		senderName = key
	}
	line := fmt.Sprintf("<< %s [%s]: %s\n", t.Format(timeLayout), senderName, text)
	return l.write(filepath.Join("dm", key), line)
}

// LogDMOutgoing records a direct message sent to a contact. The file is named
// after contactKey's prefix so it matches the corresponding incoming messages.
func (l *Logger) LogDMOutgoing(contactKey []byte, text string, t time.Time) error {
	line := fmt.Sprintf(">> %s [Me]: %s\n", t.Format(timeLayout), text)
	return l.write(filepath.Join("dm", dmFileKey(contactKey)), line)
}

// dmFileKey returns the lowercase hex of the first keyPrefixLen bytes of key,
// used as the per-contact DM log file name. Falls back to "unknown" for an
// empty key so a write never targets a malformed path.
func dmFileKey(key []byte) string {
	if len(key) == 0 {
		return "unknown"
	}
	if len(key) > keyPrefixLen {
		key = key[:keyPrefixLen]
	}
	return hex.EncodeToString(key)
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

	// Sender names and message text are attacker-controlled on a public mesh:
	// a raw newline would forge an extra entry that the readers (and .last/.top)
	// parse back as authentic history. Every Log* line is single-line by design;
	// the trailing newline is stripped here and re-added by Fprintln below.
	line = helpers.SingleLine(strings.TrimSuffix(line, "\n"))

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

	if _, err := fmt.Fprintln(f, line); err != nil {
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
