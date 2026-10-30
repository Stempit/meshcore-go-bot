// Package netgate bounds how many commands block on slow network calls at
// once. The dispatcher's worker pool is shared with fast commands like .ping;
// without a gate, a handful of concurrent AI queries can pin every worker for
// minutes and queue everything else behind them.
package netgate

// maxConcurrent is how many network-bound command handlers may block at once.
const maxConcurrent = 4

var sem = make(chan struct{}, maxConcurrent)

// TryAcquire takes one of the limited network slots, or reports false when
// all are taken — callers reply "busy" immediately instead of parking a
// worker. The returned release function must be called exactly once per
// successful acquire, typically via defer.
func TryAcquire() (release func(), ok bool) {
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	default:
		return nil, false
	}
}
