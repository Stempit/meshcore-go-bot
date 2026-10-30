package bbs

import (
	"strings"
	"testing"
	"time"
)

// Regression: a threads.json containing a zero-post thread used to panic
// bbsList (Posts[0]). validThreads must drop it at load, and bbsList must
// survive one anyway.
func TestValidThreadsDropsEmpty(t *testing.T) {
	now := time.Now()
	threads := []*BBSThread{
		{ID: 1, Title: "empty", CreatedAt: now},
		{ID: 2, Title: "ok", CreatedAt: now, Posts: []BBSPost{{Num: 1}}},
	}
	kept := validThreads(threads)
	if len(kept) != 1 || kept[0].ID != 2 {
		t.Fatalf("kept = %+v, want only the thread with posts", kept)
	}
}

func TestBBSListSurvivesEmptyThread(t *testing.T) {
	saved := bbsThreads
	bbsThreads = []*BBSThread{{ID: 1, Title: "poison", CreatedAt: time.Now()}}
	defer func() { bbsThreads = saved }()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("bbsList panicked on a zero-post thread: %v", r)
		}
	}()
	out := bbsList("ru", "/ls")
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "1/1") {
		t.Errorf("unexpected list output: %q", joined)
	}
}
