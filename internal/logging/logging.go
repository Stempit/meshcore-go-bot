// Package logging provides a centralized logger factory for all app modules.
// Call For("module-name") once at package level; the returned getter is lazy
// and captures slog.Default() on first use, after main has called SetDefault.
package logging

import (
	"log/slog"
	"sync"
)

// For returns a lazy *slog.Logger getter seeded with the given module name.
// Assign the result to a package-level var and call it at log sites:
//
//	var log = logging.For("commands")
//	log().Info("started")
func For(module string) func() *slog.Logger {
	return sync.OnceValue(func() *slog.Logger {
		return slog.Default().With("module", module)
	})
}
