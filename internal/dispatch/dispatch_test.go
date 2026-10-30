package dispatch

import "testing"

// TestSafeProcessRecoversPanic pins the worker-pool panic isolation: a plugin
// bug parsing attacker-controlled input must degrade to a log line, not kill
// the process.
func TestSafeProcessRecoversPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("safeProcess must swallow panics, got %v", r)
		}
	}()
	safeProcess(func() {
		var s []int
		_ = s[3] // the path.go:549 / bbs.go:207 class of bug
	})
}

func TestSafeProcessRunsNormally(t *testing.T) {
	ran := false
	safeProcess(func() { ran = true })
	if !ran {
		t.Fatal("process did not run")
	}
}
