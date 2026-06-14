package commands

import (
	"sync"
	"time"
)

// MigrationNotifier decides when to nudge a node that still uses 1-byte routing
// hashes to switch to 2-byte. It is signal-agnostic: callers decode the routing
// hash size from whatever they have (an advert's encoded path_len byte or a
// channel message's path bytes) and pass it in. Each node is warned at most once
// per cooldown. State is in-memory only — a restart may at worst re-send one
// warning per node, which is harmless.
type MigrationNotifier struct {
	mu       sync.Mutex
	lastWarn map[string]time.Time

	enabled  bool
	message  string
	cooldown time.Duration
}

// NewMigrationNotifier returns a ready-to-use notifier. message is the reminder
// text and cooldown is the minimum gap between warnings to the same node.
func NewMigrationNotifier(enabled bool, message string, cooldown time.Duration) *MigrationNotifier {
	return &MigrationNotifier{
		lastWarn: make(map[string]time.Time),
		enabled:  enabled,
		message:  message,
		cooldown: cooldown,
	}
}

// Message returns the configured reminder text.
func (n *MigrationNotifier) Message() string { return n.message }

// ShouldWarn reports whether the node identified by pubKeyHex should be warned
// now. hashSize is the node's routing bytes-per-hop (1, 2, …) or <=0 when it is
// unknown (e.g. a direct or flood packet that carries no hops). Only nodes on a
// 1-byte hash are warned, and never more than once per cooldown. A true result
// records the warning time, so callers should treat it as "send now".
func (n *MigrationNotifier) ShouldWarn(pubKeyHex string, hashSize int) bool {
	if !n.enabled || hashSize != 1 || pubKeyHex == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if last, ok := n.lastWarn[pubKeyHex]; ok && time.Since(last) < n.cooldown {
		return false
	}
	n.lastWarn[pubKeyHex] = time.Now()
	return true
}
