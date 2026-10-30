package webcache

import (
	"errors"
	"testing"
	"time"
)

func TestCacheFreshWithinTTL(t *testing.T) {
	calls := 0
	c := New(10*time.Minute, func() (int, error) {
		calls++
		return calls, nil
	})
	for i := 0; i < 3; i++ {
		v, err := c.Get()
		if err != nil || v != 1 {
			t.Fatalf("get %d: v=%d err=%v, want cached 1", i, v, err)
		}
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
}

func TestCacheRefetchesAfterTTL(t *testing.T) {
	calls := 0
	c := New(time.Nanosecond, func() (int, error) {
		calls++
		return calls, nil
	})
	c.Get()
	time.Sleep(2 * time.Nanosecond)
	v, err := c.Get()
	if err != nil || v != 2 {
		t.Fatalf("v=%d err=%v, want refetch to 2", v, err)
	}
}

// A network blip must not become a user-visible failure while a previous good
// value exists: the stale snapshot is served.
func TestCacheServesStaleOnFetchError(t *testing.T) {
	good, failing := true, false
	c := New(time.Nanosecond, func() (int, error) {
		time.Sleep(time.Nanosecond)
		if failing {
			return 0, errors.New("down")
		}
		return 42, nil
	})
	_ = good
	if v, err := c.Get(); err != nil || v != 42 {
		t.Fatalf("initial: v=%d err=%v", v, err)
	}
	failing = true
	time.Sleep(time.Nanosecond)
	if v, err := c.Get(); err != nil || v != 42 {
		t.Fatalf("stale: v=%d err=%v, want stale 42 and no error", v, err)
	}
}

func TestCachePropagatesErrorWithoutValue(t *testing.T) {
	boom := errors.New("boom")
	c := New(time.Minute, func() (int, error) { return 0, boom })
	if _, err := c.Get(); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestKeyedPerKeyAndBound(t *testing.T) {
	calls := map[string]int{}
	c := NewKeyed(time.Minute, 2, func(k string) (int, error) {
		calls[k]++
		return len(k), nil
	})
	if v, _ := c.Get("a"); v != 1 {
		t.Fatalf("a = %d", v)
	}
	if v, _ := c.Get("a"); v != 1 {
		t.Fatalf("a cached = %d", v)
	}
	if calls["a"] != 1 {
		t.Fatalf("a fetched %d times", calls["a"])
	}
	if v, _ := c.Get("bb"); v != 2 {
		t.Fatalf("bb = %d", v)
	}
	if calls["bb"] != 1 {
		t.Fatalf("bb fetched %d times", calls["bb"])
	}

	// Bounded: the third key clears the map, so "a" is fetched again.
	if _, _ = c.Get("ccc"); calls["a"] != 1 {
		t.Fatalf("clear must not refetch eagerly")
	}
	if _, _ = c.Get("a"); calls["a"] != 2 {
		t.Fatalf("a must refetch after eviction, calls=%d", calls["a"])
	}
}

func TestKeyedServesStaleOnFetchError(t *testing.T) {
	fail := false
	c := NewKeyed(time.Nanosecond, 0, func(string) (int, error) {
		time.Sleep(time.Nanosecond)
		if fail {
			return 0, errors.New("down")
		}
		return 7, nil
	})
	if v, _ := c.Get("k"); v != 7 {
		t.Fatalf("initial v=%d", v)
	}
	fail = true
	time.Sleep(time.Nanosecond)
	if v, err := c.Get("k"); err != nil || v != 7 {
		t.Fatalf("stale: v=%d err=%v, want stale 7", v, err)
	}
}
