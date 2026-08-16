// Package ticket answers .ticket by assigning each sender a stable random
// "callsign" phrase, persisted to disk.
package ticket

import (
	"bufio"
	"embed"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"

	"meshbot/internal/support/i18n"
	"meshbot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]string // senderKey -> phrase
	path    string
}

var ticketStore *TicketStore

// InitTicketStore loads (or creates) the ticket persistence file.
func InitTicketStore(path string) error {
	ts := &TicketStore{
		tickets: make(map[string]string),
		path:    path,
	}
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("opening ticket store: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		key, phrase, ok := strings.Cut(line, "\t")
		if !ok || key == "" || phrase == "" {
			continue
		}
		ts.tickets[key] = phrase
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	ticketStore = ts
	return nil
}

func (ts *TicketStore) getOrCreate(lang, senderKey string) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	key := printableKey(senderKey)
	if phrase, ok := ts.tickets[key]; ok {
		return phrase, nil
	}
	first := tr.TList(lang, "first")
	second := tr.TList(lang, "second")
	phrase := first[rand.IntN(len(first))] + " " + second[rand.IntN(len(second))]
	ts.tickets[key] = phrase
	f, err := os.OpenFile(ts.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return "", fmt.Errorf("writing ticket store: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\n", key, phrase)
	return phrase, err
}

func (ts *TicketStore) random() (string, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.tickets) == 0 {
		return "", false
	}
	phrases := make([]string, 0, len(ts.tickets))
	for _, p := range ts.tickets {
		phrases = append(phrases, p)
	}
	return phrases[rand.IntN(len(phrases))], true
}

// printableKey returns senderKey as a readable string.
// The DM path passes raw public-key bytes cast to string, so we hex-encode
// those; plain channel names (already ASCII) pass through unchanged.
func printableKey(key string) string {
	for _, b := range []byte(key) {
		if b < 32 || b > 126 {
			return hex.EncodeToString([]byte(key))
		}
	}
	return key
}

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:     "ticket",
		Triggers: []string{".ticket"},
		Match:    registry.Prefix,
		// Not listed in the .help index (no HelpShort), matching the original.
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	reply, err := processTicket(c.Lang, c.Text, c.SenderKey)
	if err != nil {
		return []string{tr.T(c.Lang, "error")}, err
	}
	return []string{reply}, nil
}

func processTicket(lang, text, senderKey string) (string, error) {
	if ticketStore == nil {
		return tr.T(lang, "not_initialized"), nil
	}
	arg := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(text)), ".ticket"))
	if arg == "random" {
		phrase, ok := ticketStore.random()
		if !ok {
			return tr.T(lang, "no_callsigns"), nil
		}
		return tr.T(lang, "chosen", phrase), nil
	}
	if senderKey == "" {
		return tr.T(lang, "no_key"), nil
	}
	phrase, err := ticketStore.getOrCreate(lang, senderKey)
	if err != nil {
		return "", err
	}
	return tr.T(lang, "your_callsign", phrase), nil
}
