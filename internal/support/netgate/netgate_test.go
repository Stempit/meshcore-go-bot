package netgate

import "testing"

func TestTryAcquireBoundsConcurrency(t *testing.T) {
	var releases []func()
	for i := 0; i < maxConcurrent; i++ {
		release, ok := TryAcquire()
		if !ok {
			t.Fatalf("acquire %d of %d must succeed", i+1, maxConcurrent)
		}
		releases = append(releases, release)
	}

	if release, ok := TryAcquire(); ok {
		t.Fatal("acquire beyond the limit must fail")
	} else if release != nil {
		t.Error("failed acquire must return a nil release")
	}

	// Releasing one slot makes room for exactly one more.
	releases[0]()
	if release, ok := TryAcquire(); !ok {
		t.Fatal("acquire after release must succeed")
	} else {
		release()
	}

	for _, r := range releases[1:] {
		r()
	}
	if release, ok := TryAcquire(); !ok {
		t.Fatal("all slots must be free again")
	} else {
		release()
	}
}
