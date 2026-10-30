// Package webcache provides small TTL caches around fetch functions, so
// commands that query slow, rate-limited external services (wttr.in, analyzer
// stats) don't hammer them on every radio command and don't turn one network
// blip into a user-visible failure while fresh-enough data exists.
package webcache

import (
	"sync"
	"time"
)

// Cache memoizes the result of one fetch for ttl. On fetch error the last
// good value is served stale.
type Cache[T any] struct {
	ttl time.Duration
	fn  func() (T, error)

	mu      sync.Mutex
	val     T
	expires time.Time
	have    bool
}

// New builds a cache that refetches via fn when the previous value is older
// than ttl.
func New[T any](ttl time.Duration, fn func() (T, error)) *Cache[T] {
	return &Cache[T]{ttl: ttl, fn: fn}
}

// Get returns the cached value while fresh; re-fetches otherwise, serving the
// stale value when the refresh fails (the fetch function reports the error to
// its own log).
func (c *Cache[T]) Get() (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.have && time.Now().Before(c.expires) {
		return c.val, nil
	}
	val, err := c.fn()
	if err != nil {
		if c.have {
			return c.val, nil
		}
		var zero T
		return zero, err
	}
	c.val, c.expires, c.have = val, time.Now().Add(c.ttl), true
	return c.val, nil
}

// Keyed caches values per key with a TTL, bounded by max keys — a full map is
// cleared wholesale, entries are cheap to recompute. fn must handle the key
// itself (unknown keys are not an error here; the fetch decides).
type Keyed[T any] struct {
	ttl time.Duration
	max int
	fn  func(key string) (T, error)

	mu   sync.Mutex
	vals map[string]keyedEntry[T]
}

type keyedEntry[T any] struct {
	val     T
	expires time.Time
}

// NewKeyed builds a keyed cache; max caps memory (0 = unbounded).
func NewKeyed[T any](ttl time.Duration, max int, fn func(key string) (T, error)) *Keyed[T] {
	return &Keyed[T]{ttl: ttl, max: max, fn: fn, vals: map[string]keyedEntry[T]{}}
}

// Get returns the cached value for key while fresh; re-fetches otherwise,
// serving the stale value when the refresh fails.
func (k *Keyed[T]) Get(key string) (T, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if e, ok := k.vals[key]; ok && time.Now().Before(e.expires) {
		return e.val, nil
	}
	val, err := k.fn(key)
	if err != nil {
		if e, ok := k.vals[key]; ok {
			return e.val, nil
		}
		var zero T
		return zero, err
	}
	if k.max > 0 && len(k.vals) >= k.max {
		k.vals = map[string]keyedEntry[T]{} // bounded memory over hit-rate
	}
	k.vals[key] = keyedEntry[T]{val: val, expires: time.Now().Add(k.ttl)}
	return val, nil
}
