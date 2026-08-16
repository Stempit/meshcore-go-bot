package alerts

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SeenSet tracks which incident permalinks have already been processed. It
// persists them to a file so state survives restarts.
//
// File format — one permalink per line.
type SeenSet struct {
	mu   sync.Mutex
	ids  map[string]struct{}
	path string
}

// NewSeenSet loads the seen-ids file (creating it and its parent directory if
// absent) and returns a ready-to-use SeenSet.
func NewSeenSet(path string) (*SeenSet, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating alerts log dir: %w", err)
		}
	}

	s := &SeenSet{ids: make(map[string]struct{}), path: path}
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening seen-ids file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		id := strings.TrimSpace(scanner.Text())
		if id != "" {
			s.ids[id] = struct{}{}
		}
	}
	return s, scanner.Err()
}

// Empty reports whether no ids have been recorded yet (used to detect a
// first-ever run, so history isn't dumped into the channel).
func (s *SeenSet) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids) == 0
}

// CheckAndAdd returns true if id was already known. If unknown, it adds it to
// the in-memory set and appends it to the file.
func (s *SeenSet) CheckAndAdd(id string) (alreadyKnown bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.ids[id]; ok {
		return true, nil
	}
	s.ids[id] = struct{}{}

	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return false, fmt.Errorf("writing seen-ids file: %w", err)
	}
	defer f.Close()

	_, err = fmt.Fprintln(f, id)
	return false, err
}
