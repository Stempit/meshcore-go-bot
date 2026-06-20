// Package bbs is a stateful interceptor: once a sender enters BBS mode (.2ch)
// it claims all of that sender's input until they .exit. It registers at a
// Priority above normal commands (but below claw) so the interception wins.
package bbs

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"meshbot/internal/commands/registry"
)

const (
	bbsDir          = "log/2ch"
	bbsThreadsFile  = "log/2ch/threads.json"
	bbsPageSize     = 8 // threads per /ls page
	bbsPostsPerPage = 3 // posts per /rd page
	bbsMsgMax       = 138
	// bbsPriority sits above normal commands (0) but below claw (20).
	bbsPriority = 10
)

// BBSPost is a single post in a thread.
type BBSPost struct {
	Num        int       `json:"num"`
	AuthorKey  string    `json:"author_key"`
	AuthorName string    `json:"author_name"`
	CreatedAt  time.Time `json:"created_at"`
	Text       string    `json:"text"`
}

// BBSThread is a BBS thread.
type BBSThread struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Posts     []BBSPost `json:"posts"`
}

var (
	bbsSessions   = map[string]bool{}
	bbsSessionsMu sync.RWMutex

	bbsThreads   []*BBSThread
	bbsThreadsMu sync.Mutex
	bbsNextID    int
	bbsLoaded    bool
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "bbs",
		Triggers:  []string{".2ch"},
		Match:     registry.Exact,
		HelpShort: "BBS mode",
		HelpLong:  ".2ch - start BBS/forum mode",
		Priority:  bbsPriority,
	}
}

// Matches claims ".2ch" (enter) plus any input while the sender is in BBS mode.
func (plugin) Matches(c registry.Ctx) bool {
	return isBBSMode(c.SenderKey) || c.Lower == ".2ch"
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	if isBBSMode(c.SenderKey) {
		if c.Lower == ".exit" {
			exitBBSMode(c.SenderKey)
			return []string{"Left BBS. Bye!"}, nil
		}
		return processBBSCommand(c.Text, c.SenderKey, c.SenderName)
	}
	// Enter BBS mode.
	enterBBSMode(c.SenderKey)
	return []string{"Welcome to BBS!\n/hl for help\n.exit to leave"}, nil
}

// isBBSMode returns true when the session key is in BBS mode.
func isBBSMode(sessionKey string) bool {
	bbsSessionsMu.RLock()
	defer bbsSessionsMu.RUnlock()
	return bbsSessions[sessionKey]
}

// enterBBSMode puts the session key into BBS mode.
func enterBBSMode(sessionKey string) {
	bbsSessionsMu.Lock()
	defer bbsSessionsMu.Unlock()
	bbsSessions[sessionKey] = true
}

// exitBBSMode removes the session key from BBS mode.
func exitBBSMode(sessionKey string) {
	bbsSessionsMu.Lock()
	defer bbsSessionsMu.Unlock()
	delete(bbsSessions, sessionKey)
}

// processBBSCommand routes a command while in BBS mode.
func processBBSCommand(text, authorKey, authorName string) ([]string, error) {
	bbsThreadsMu.Lock()
	defer bbsThreadsMu.Unlock()

	if err := bbsLoad(); err != nil {
		return []string{"ERR: cannot load threads"}, err
	}

	lower := strings.ToLower(strings.TrimSpace(text))

	switch {
	case lower == "/hl" || strings.HasPrefix(lower, "/hl "):
		return bbsHelp(), nil
	case lower == "/ls" || strings.HasPrefix(lower, "/ls "):
		return bbsList(text), nil
	case strings.HasPrefix(lower, "/rd"):
		return bbsRead(text), nil
	case strings.HasPrefix(lower, "/nw"):
		return bbsNew(text, authorKey, authorName), nil
	case strings.HasPrefix(lower, "/rp"):
		return bbsReply(text, authorKey, authorName), nil
	default:
		return []string{"Unknown cmd. /hl for help"}, nil
	}
}

func bbsHelp() []string {
	return []string{
		"/ls [page] - list threads",
		"/rd <id> [page] - read thread",
		"/nw <title>|<text> - new thread",
		"/rp <id>|<text> - reply to thread",
		".exit - leave BBS",
	}
}

func bbsList(text string) []string {
	parts := strings.Fields(text)
	page := 1
	if len(parts) >= 2 {
		fmt.Sscanf(parts[1], "%d", &page)
		if page < 1 {
			page = 1
		}
	}

	if len(bbsThreads) == 0 {
		return []string{"No threads yet. /nw title|text"}
	}

	// Sort by last activity (newest first).
	sorted := make([]*BBSThread, len(bbsThreads))
	copy(sorted, bbsThreads)
	sort.Slice(sorted, func(i, j int) bool {
		ti, tj := sorted[i].CreatedAt, sorted[j].CreatedAt
		if n := len(sorted[i].Posts); n > 0 {
			ti = sorted[i].Posts[n-1].CreatedAt
		}
		if n := len(sorted[j].Posts); n > 0 {
			tj = sorted[j].Posts[n-1].CreatedAt
		}
		return ti.After(tj)
	})

	totalPages := int(math.Ceil(float64(len(sorted)) / float64(bbsPageSize)))
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * bbsPageSize
	end := start + bbsPageSize
	if end > len(sorted) {
		end = len(sorted)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Page %d/%d:", page, totalPages))
	for _, t := range sorted[start:end] {
		label := "NEW"
		if len(t.Posts) > 1 {
			label = fmt.Sprintf("RE:%d", len(t.Posts)-1)
		}
		author := bbsAuthor(t.Posts[0].AuthorKey, t.Posts[0].AuthorName)
		title := t.Title
		if len(title) > 20 {
			title = title[:18] + ".."
		}
		lines = append(lines, fmt.Sprintf("[%d][%s] %s (%s)", t.ID, label, title, author))
	}
	if page < totalPages {
		lines = append(lines, fmt.Sprintf("/ls %d - next page", page+1))
	}

	return bbsPackLines(lines)
}

func bbsRead(text string) []string {
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return []string{"Usage: /rd <id> [page]"}
	}

	var id int
	if _, err := fmt.Sscanf(parts[1], "%d", &id); err != nil {
		return []string{"Invalid thread ID"}
	}

	var thread *BBSThread
	for _, t := range bbsThreads {
		if t.ID == id {
			thread = t
			break
		}
	}
	if thread == nil {
		return []string{fmt.Sprintf("Thread %d not found", id)}
	}

	totalPages := int(math.Ceil(float64(len(thread.Posts)) / float64(bbsPostsPerPage)))
	if totalPages == 0 {
		totalPages = 1
	}

	page := totalPages // default: last page
	if len(parts) >= 3 {
		fmt.Sscanf(parts[2], "%d", &page)
		if page < 1 {
			page = 1
		}
		if page > totalPages {
			page = totalPages
		}
	}

	start := (page - 1) * bbsPostsPerPage
	end := start + bbsPostsPerPage
	if end > len(thread.Posts) {
		end = len(thread.Posts)
	}

	var msgs []string
	title := thread.Title
	if len(title) > 25 {
		title = title[:23] + ".."
	}
	msgs = append(msgs, fmt.Sprintf("[Thr %d] %s (p%d/%d)", id, title, page, totalPages))

	for _, post := range thread.Posts[start:end] {
		author := bbsAuthor(post.AuthorKey, post.AuthorName)
		ago := bbsTimeAgo(post.CreatedAt)
		header := fmt.Sprintf("Post #%d by %s (%s):", post.Num, author, ago)
		// One logical message per post; long posts are split at the send site.
		msgs = append(msgs, header+"\n"+post.Text)
	}

	msgs = append(msgs, fmt.Sprintf("Reply: /rp %d|text", id))
	return msgs
}

func bbsNew(text, authorKey, authorName string) []string {
	rest := strings.TrimSpace(text[3:])
	if rest == "" {
		return []string{"Usage: /nw title|text"}
	}
	idx := strings.Index(rest, "|")
	if idx < 0 {
		return []string{"Usage: /nw title|text"}
	}
	title := strings.TrimSpace(rest[:idx])
	body := strings.TrimSpace(rest[idx+1:])
	if title == "" || body == "" {
		return []string{"Title and text required"}
	}

	now := time.Now()
	t := &BBSThread{
		ID:        bbsNextID,
		Title:     title,
		CreatedAt: now,
		Posts: []BBSPost{{
			Num:        1,
			AuthorKey:  authorKey,
			AuthorName: authorName,
			CreatedAt:  now,
			Text:       body,
		}},
	}
	bbsNextID++
	bbsThreads = append(bbsThreads, t)
	if err := bbsSave(); err != nil {
		return []string{"ERR: cannot save thread"}
	}
	return []string{fmt.Sprintf("Thread %d created: %s", t.ID, title)}
}

func bbsReply(text, authorKey, authorName string) []string {
	rest := strings.TrimSpace(text[3:])
	if rest == "" {
		return []string{"Usage: /rp id|text"}
	}
	idx := strings.Index(rest, "|")
	if idx < 0 {
		return []string{"Usage: /rp id|text"}
	}
	idStr := strings.TrimSpace(rest[:idx])
	body := strings.TrimSpace(rest[idx+1:])
	if body == "" {
		return []string{"Reply text required"}
	}

	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		return []string{"Invalid thread ID"}
	}

	for _, t := range bbsThreads {
		if t.ID == id {
			num := len(t.Posts) + 1
			t.Posts = append(t.Posts, BBSPost{
				Num:        num,
				AuthorKey:  authorKey,
				AuthorName: authorName,
				CreatedAt:  time.Now(),
				Text:       body,
			})
			if err := bbsSave(); err != nil {
				return []string{"ERR: cannot save reply"}
			}
			return []string{fmt.Sprintf("Reply #%d added to thread %d", num, id)}
		}
	}
	return []string{fmt.Sprintf("Thread %d not found", id)}
}

// bbsAuthor returns a display name: username or first 6 hex chars of key.
func bbsAuthor(key, name string) string {
	if name != "" {
		if len(name) > 8 {
			return name[:8]
		}
		return name
	}
	if len(key) >= 6 {
		return key[:6]
	}
	return key
}

func bbsTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// bbsPackLines packs text lines into messages, each fitting bbsMsgMax chars.
func bbsPackLines(lines []string) []string {
	var result []string
	cur := ""
	for _, line := range lines {
		if cur == "" {
			cur = line
		} else if len(cur)+1+len(line) <= bbsMsgMax {
			cur += "\n" + line
		} else {
			result = append(result, cur)
			cur = line
		}
	}
	if cur != "" {
		result = append(result, cur)
	}
	return result
}

// bbsLoad loads threads from disk if not already loaded. Caller must hold bbsThreadsMu.
func bbsLoad() error {
	if bbsLoaded {
		return nil
	}
	if err := os.MkdirAll(bbsDir, 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(bbsThreadsFile)
	if os.IsNotExist(err) {
		bbsThreads = []*BBSThread{}
		bbsNextID = 1
		bbsLoaded = true
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &bbsThreads); err != nil {
		return err
	}
	bbsNextID = 1
	for _, t := range bbsThreads {
		if t.ID >= bbsNextID {
			bbsNextID = t.ID + 1
		}
	}
	bbsLoaded = true
	return nil
}

// bbsSave serialises threads to disk. Caller must hold bbsThreadsMu.
func bbsSave() error {
	data, err := json.MarshalIndent(bbsThreads, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(bbsThreadsFile, data, 0o644)
}
