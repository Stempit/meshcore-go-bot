package registry

import (
	"sync"
	"time"
)

// redirectCooldown throttles redirect notices so that repeated commands on a
// busy channel do not turn the notice itself into flood. It is keyed by
// channel+command name and is process-global (one bot, one set of channels).
var redirectCooldown = struct {
	sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

// allowNotice reports whether a redirect notice for (channel, name) may be sent
// now, given cooldown seconds between notices. It stamps the time on success.
// cooldown <= 0 disables throttling (always allowed).
func allowNotice(channel, name string, cooldown int) bool {
	if cooldown <= 0 {
		return true
	}
	key := channel + "\x00" + name
	now := time.Now()

	redirectCooldown.Lock()
	defer redirectCooldown.Unlock()
	if last, ok := redirectCooldown.last[key]; ok && now.Sub(last) < time.Duration(cooldown)*time.Second {
		return false
	}
	redirectCooldown.last[key] = now
	return true
}
