package commands

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
)

// KnownKeys tracks which node public keys have already been greeted.
// It persists them to a file so state survives restarts.
//
// File format — one entry per line:
//
//	<pubkeyhex> # NodeName
//
// The "# NodeName" part is optional. Lines starting with # are comments.
type KnownKeys struct {
	mu   sync.Mutex
	keys map[string]struct{}
	path string
}

// NewKnownKeys loads the known-keys file (creating it if absent) and returns
// a ready-to-use KnownKeys.
func NewKnownKeys(path string) (*KnownKeys, error) {
	kk := &KnownKeys{
		keys: make(map[string]struct{}),
		path: path,
	}
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening known-keys file: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// extract the key (everything before optional " # ...")
		key := line
		if idx := strings.Index(line, "#"); idx >= 0 {
			key = strings.TrimSpace(line[:idx])
		}
		if key != "" {
			kk.keys[key] = struct{}{}
		}
	}
	return kk, scanner.Err()
}

// CheckAndAdd returns true if the key was already known.
// If unknown, adds it to the in-memory set and appends a line to the file.
func (kk *KnownKeys) CheckAndAdd(pubKey []byte, name string) (alreadyKnown bool, err error) {
	id := hex.EncodeToString(pubKey)
	kk.mu.Lock()
	defer kk.mu.Unlock()
	if _, ok := kk.keys[id]; ok {
		return true, nil
	}
	kk.keys[id] = struct{}{}
	f, err := os.OpenFile(kk.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return false, fmt.Errorf("writing known-keys file: %w", err)
	}
	defer f.Close()
	line := id
	if name != "" {
		line += " # " + name
	}
	_, err = fmt.Fprintln(f, line)
	return false, err
}

// GreetMessage returns the greeting text for a newly seen node.
// If name is empty, the first 8 hex characters of pubKey are used as the display name.
//
//	contactType: 1 = chat/companion, 2 = repeater, 3 = room server, 4 = sensor
func GreetMessage(name string, pubKey []byte, contactType int) string {
	if name == "" {
		name = hex.EncodeToString(pubKey)
		if len(name) > 8 {
			name = name[:8]
		}
	}
	switch contactType {
	case 2:
		return fmt.Sprintf("Wow! New repeater found: @[%s]", name)
	case 3:
		return fmt.Sprintf("New room server found: @[%s]", name)
	case 4:
		return fmt.Sprintf("New sensor found: @[%s]", name)
	default:
		return fmt.Sprintf("@[%s] welcome!", name)
	}
}
