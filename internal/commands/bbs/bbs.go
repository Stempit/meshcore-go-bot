// Package bbs is a stateful interceptor: once a sender enters BBS mode (.2ch)
// it claims all of that sender's input until they .exit. It registers at a
// Priority above normal commands (but below agent) so the interception wins.
package bbs

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stempit/meshcore-go-bot/internal/support/helpers"
	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

//go:embed lang/*.yaml
var langFS embed.FS

var tr = i18n.MustLoad(langFS)

// Threads live under log/2ch; vars (not consts) so tests can redirect them.
var (
	bbsDir          = "log/2ch"
	bbsThreadsFile  = "log/2ch/threads.json"
	bbsPageSize     = 8 // threads per /ls page
	bbsPostsPerPage = 3 // posts per /rd page
	bbsMsgMax       = 138
	bbsMaxThreads   = 50 // forum growth cap: oldest activity pruned at save
	// bbsPriority sits above normal commands (0) but below agent (20).
	bbsPriority = 10
)

// bbsSessionTTL is how long a BBS session survives without activity: someone
// who forgets .exit must not have their input swallowed forever.
const bbsSessionTTL = 30 * time.Minute

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
	bbsSessions   = map[string]time.Time{} // session key -> last activity
	bbsSessionsMu sync.Mutex

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
		Group:     "Other",
		Priority:  bbsPriority,
	}
}

// LocalizedHelp returns the per-language long help for ".2ch help".
func (plugin) LocalizedHelp(lang string) string { return tr.T(lang, "help.long") }

// Matches claims ".2ch" (enter) plus any input while the sender is in BBS mode.
func (plugin) Matches(c registry.Ctx) bool {
	return isBBSMode(c.SenderKey) || c.Lower == ".2ch"
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	if isBBSMode(c.SenderKey) {
		touchBBSMode(c.SenderKey)
		if c.Lower == ".exit" {
			exitBBSMode(c.SenderKey)
			return []string{tr.T(c.Lang, "left")}, nil
		}
		// Trimmed, not Text: the sub-command parsers slice fixed byte offsets
		// ("/nw …"), which is only correct when leading space is already gone.
		return processBBSCommand(c.Lang, c.Trimmed, c.SenderKey, c.SenderName)
	}
	// Enter BBS mode. Unidentified senders are refused: sessions are keyed on
	// SenderKey, which is "" for anyone the bot cannot resolve — one anonymous
	// ".2ch" would otherwise capture every unidentified sender bot-wide.
	if c.SenderKey == "" {
		return []string{tr.T(c.Lang, "need_key")}, nil
	}
	enterBBSMode(c.SenderKey)
	return []string{tr.T(c.Lang, "welcome")}, nil
}

// isBBSMode reports whether the session key is in BBS mode, lazily dropping
// sessions idle past bbsSessionTTL.
func isBBSMode(sessionKey string) bool {
	bbsSessionsMu.Lock()
	defer bbsSessionsMu.Unlock()
	last, ok := bbsSessions[sessionKey]
	if !ok {
		return false
	}
	if time.Since(last) > bbsSessionTTL {
		delete(bbsSessions, sessionKey)
		return false
	}
	return true
}

// touchBBSMode records activity for the session key, renewing its TTL.
func touchBBSMode(sessionKey string) {
	bbsSessionsMu.Lock()
	defer bbsSessionsMu.Unlock()
	bbsSessions[sessionKey] = time.Now()
}

// enterBBSMode puts the session key into BBS mode.
func enterBBSMode(sessionKey string) {
	bbsSessionsMu.Lock()
	defer bbsSessionsMu.Unlock()
	bbsSessions[sessionKey] = time.Now()
}

// exitBBSMode removes the session key from BBS mode.
func exitBBSMode(sessionKey string) {
	bbsSessionsMu.Lock()
	defer bbsSessionsMu.Unlock()
	delete(bbsSessions, sessionKey)
}

// processBBSCommand routes a command while in BBS mode.
func processBBSCommand(lang, text, authorKey, authorName string) ([]string, error) {
	bbsThreadsMu.Lock()
	defer bbsThreadsMu.Unlock()

	if err := bbsLoad(); err != nil {
		return []string{tr.T(lang, "err_load")}, err
	}

	lower := strings.ToLower(strings.TrimSpace(text))

	// Sub-command matching follows the registry's prefix rule: exact, or the
	// trigger followed by a space — so "/rdzone" is not parsed as "/rd zone".
	switch {
	case lower == "/hl" || strings.HasPrefix(lower, "/hl "):
		return bbsHelp(lang), nil
	case lower == "/ls" || strings.HasPrefix(lower, "/ls "):
		return bbsList(lang, text), nil
	case lower == "/rd" || strings.HasPrefix(lower, "/rd "):
		return bbsRead(lang, text), nil
	case lower == "/nw" || strings.HasPrefix(lower, "/nw "):
		return bbsNew(lang, text, authorKey, authorName), nil
	case lower == "/rp" || strings.HasPrefix(lower, "/rp "):
		return bbsReply(lang, text, authorKey, authorName), nil
	default:
		return []string{tr.T(lang, "unknown_cmd")}, nil
	}
}

func bbsHelp(lang string) []string {
	return []string{
		tr.T(lang, "help.hl_ls"),
		tr.T(lang, "help.hl_rd"),
		tr.T(lang, "help.hl_nw"),
		tr.T(lang, "help.hl_rp"),
		tr.T(lang, "help.hl_exit"),
	}
}

func bbsList(lang, text string) []string {
	parts := strings.Fields(text)
	page := 1
	if len(parts) >= 2 {
		fmt.Sscanf(parts[1], "%d", &page)
		if page < 1 {
			page = 1
		}
	}

	if len(bbsThreads) == 0 {
		return []string{tr.T(lang, "no_threads")}
	}

	// Sort by last activity (newest first).
	sorted := make([]*BBSThread, len(bbsThreads))
	copy(sorted, bbsThreads)
	sort.Slice(sorted, func(i, j int) bool {
		return lastActivity(sorted[i]).After(lastActivity(sorted[j]))
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
	lines = append(lines, tr.T(lang, "page_header", page, totalPages))
	for _, t := range sorted[start:end] {
		if len(t.Posts) == 0 { // belt-and-braces: validThreads drops these at load
			continue
		}
		label := tr.T(lang, "label_new")
		if len(t.Posts) > 1 {
			label = tr.T(lang, "label_re", len(t.Posts)-1)
		}
		author := bbsAuthor(t.Posts[0].AuthorKey, t.Posts[0].AuthorName)
		title := helpers.ClipRunes(t.Title, 18)
		lines = append(lines, tr.T(lang, "list_line", t.ID, label, title, author))
	}
	if page < totalPages {
		lines = append(lines, tr.T(lang, "next_page", page+1))
	}

	return bbsPackLines(lines)
}

func bbsRead(lang, text string) []string {
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return []string{tr.T(lang, "usage_rd")}
	}

	var id int
	if _, err := fmt.Sscanf(parts[1], "%d", &id); err != nil {
		return []string{tr.T(lang, "invalid_id")}
	}

	var thread *BBSThread
	for _, t := range bbsThreads {
		if t.ID == id {
			thread = t
			break
		}
	}
	if thread == nil {
		return []string{tr.T(lang, "thread_not_found", id)}
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
	title := helpers.ClipRunes(thread.Title, 23)
	msgs = append(msgs, tr.T(lang, "thread_header", id, title, page, totalPages))

	for _, post := range thread.Posts[start:end] {
		author := bbsAuthor(post.AuthorKey, post.AuthorName)
		ago := bbsTimeAgo(lang, post.CreatedAt)
		header := tr.T(lang, "post_header", post.Num, author, ago)
		// One logical message per post; long posts are split at the send site.
		msgs = append(msgs, header+"\n"+post.Text)
	}

	msgs = append(msgs, tr.T(lang, "reply_hint", id))
	return msgs
}

func bbsNew(lang, text, authorKey, authorName string) []string {
	rest := strings.TrimSpace(text[3:])
	if rest == "" {
		return []string{tr.T(lang, "usage_nw")}
	}
	idx := strings.Index(rest, "|")
	if idx < 0 {
		return []string{tr.T(lang, "usage_nw")}
	}
	title := strings.TrimSpace(rest[:idx])
	body := strings.TrimSpace(rest[idx+1:])
	if title == "" || body == "" {
		return []string{tr.T(lang, "title_text_required")}
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
		return []string{tr.T(lang, "err_save_thread")}
	}
	return []string{tr.T(lang, "thread_created", t.ID, title)}
}

func bbsReply(lang, text, authorKey, authorName string) []string {
	rest := strings.TrimSpace(text[3:])
	if rest == "" {
		return []string{tr.T(lang, "usage_rp")}
	}
	idx := strings.Index(rest, "|")
	if idx < 0 {
		return []string{tr.T(lang, "usage_rp")}
	}
	idStr := strings.TrimSpace(rest[:idx])
	body := strings.TrimSpace(rest[idx+1:])
	if body == "" {
		return []string{tr.T(lang, "reply_text_required")}
	}

	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		return []string{tr.T(lang, "invalid_id")}
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
				return []string{tr.T(lang, "err_save_reply")}
			}
			return []string{tr.T(lang, "reply_added", num, id)}
		}
	}
	return []string{tr.T(lang, "thread_not_found", id)}
}

// bbsAuthor returns a display name: username or first 6 hex chars of key.
// Clipping is by runes so Cyrillic names never emit invalid UTF-8.
func bbsAuthor(key, name string) string {
	if name != "" {
		return helpers.ClipRunes(name, 8)
	}
	if len(key) >= 6 {
		return key[:6]
	}
	return key
}

// lastActivity returns the thread's most recent post time (creation time for
// a fresh thread).
func lastActivity(t *BBSThread) time.Time {
	if n := len(t.Posts); n > 0 {
		return t.Posts[n-1].CreatedAt
	}
	return t.CreatedAt
}

func bbsTimeAgo(lang string, t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return tr.T(lang, "ago_now")
	case d < time.Hour:
		return tr.T(lang, "ago_min", int(d.Minutes()))
	case d < 24*time.Hour:
		return tr.T(lang, "ago_hour", int(d.Hours()))
	default:
		return tr.T(lang, "ago_day", int(d.Hours()/24))
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
	if errors.Is(err, os.ErrNotExist) {
		// Either a fresh install or the atomic-save rename window: fall back
		// to the previous generation kept by bbsSave.
		data, err = os.ReadFile(bbsThreadsFile + ".bak")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) == 0 {
		bbsThreads = []*BBSThread{}
		bbsNextID = 1
		bbsLoaded = true
		return nil
	}
	if err := json.Unmarshal(data, &bbsThreads); err != nil {
		// The main file is corrupt; try the previous generation before
		// giving up (bbsSave keeps one around).
		bak, bakErr := os.ReadFile(bbsThreadsFile + ".bak")
		if bakErr != nil {
			return err
		}
		if bakErr := json.Unmarshal(bak, &bbsThreads); bakErr != nil {
			return err // both generations unreadable
		}
	}
	bbsThreads = validThreads(bbsThreads)
	bbsNextID = 1
	for _, t := range bbsThreads {
		if t.ID >= bbsNextID {
			bbsNextID = t.ID + 1
		}
	}
	bbsLoaded = true
	return nil
}

// validThreads drops zero-post threads: threads.json is hand-editable and
// written non-transactionally by older versions, and bbsList indexes
// Posts[0] — a single empty entry must not poison every later /ls.
func validThreads(threads []*BBSThread) []*BBSThread {
	kept := make([]*BBSThread, 0, len(threads))
	for _, t := range threads {
		if len(t.Posts) > 0 {
			kept = append(kept, t)
		}
	}
	return kept
}

// bbsSave serialises threads to disk. Caller must hold bbsThreadsMu.
// The write is atomic (temp file + rename) — these nodes run on battery, and
// a crash mid-write must not truncate the forum. The previous generation is
// kept as .bak so bbsLoad can recover from a corrupt new file.
func bbsSave() error {
	pruneThreads()
	data, err := json.MarshalIndent(bbsThreads, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bbsDir, 0o755); err != nil {
		return err
	}
	tmp := bbsThreadsFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	// Rotating the old file away first leaves a window where only .bak holds
	// the data — that is exactly what the .bak fallback in bbsLoad covers.
	if err := os.Rename(bbsThreadsFile, bbsThreadsFile+".bak"); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, bbsThreadsFile)
}

// pruneThreads caps forum growth from anonymous mesh input: beyond
// bbsMaxThreads the oldest-activity threads are dropped. Caller holds
// bbsThreadsMu. Note this reorders bbsThreads by activity — harmless, every
// consumer looks threads up by ID or sorts its own copy.
func pruneThreads() {
	if len(bbsThreads) <= bbsMaxThreads {
		return
	}
	sort.Slice(bbsThreads, func(i, j int) bool {
		return lastActivity(bbsThreads[i]).After(lastActivity(bbsThreads[j]))
	})
	bbsThreads = bbsThreads[:bbsMaxThreads]
}
