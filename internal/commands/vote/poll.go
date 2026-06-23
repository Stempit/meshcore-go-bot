package vote

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"meshbot/internal/support/games"
	"meshbot/internal/support/helpers"
)

const (
	voteDefRoundSec   = 180 // auto-close countdown when unset (3 min)
	voteDefMaxOptions = 6   // option cap when unset
	voteMinOptions    = 2   // a poll needs at least two options
	voteQuestionRune  = 40  // question clipped to this many runes (mesh size)
	voteLabelRune     = 20  // each option label clipped to this many runes
)

// poll is an open poll in a single channel. votes maps a sender's stable key to
// their chosen option index, so a re-vote moves the one vote rather than adding
// another, and the tally never reveals who voted. The round stays open until the
// timer fires or someone runs ".vote end". token guards stale timer fires the
// same way the .words round does.
type poll struct {
	question string
	options  []string       // option labels; index 0 = option 1
	votes    map[string]int // senderKey -> chosen option index
	deadline time.Time
	timer    *time.Timer
	token    uint64
}

var (
	polls     = map[string]*poll{} // keyed by channel name
	pollMu    sync.Mutex
	pollToken uint64 // monotonic poll-id source, guarded by pollMu
)

const (
	voteUsage       = "📊 .vote start вопрос | вариант1 | вариант2 — опрос; .vote <номер> — голос; .vote status / end"
	voteChannelOnly = "Голосование доступно только в каналах"
	voteNoPoll      = "Опрос не идёт. .vote start"
)

// voteRoundDur is the auto-close countdown, defaulted when unconfigured.
func voteRoundDur() time.Duration {
	sec := conf.RoundSec
	if sec <= 0 {
		sec = voteDefRoundSec
	}
	return time.Duration(sec) * time.Second
}

// voteMaxOptions is the option cap, defaulted when unconfigured.
func voteMaxOptions() int {
	n := conf.MaxOptions
	if n <= 0 {
		n = voteDefMaxOptions
	}
	return n
}

// voteStart opens a poll. spec is "вопрос | вариант1 | вариант2 [| ...]": the
// first pipe-segment is the question, the rest are the options.
func voteStart(scope, channelName, spec string) ([]string, error) {
	if strings.TrimSpace(channelName) == "" {
		return []string{voteChannelOnly}, nil
	}

	var parts []string
	for _, p := range strings.Split(spec, "|") {
		if t := strings.TrimSpace(p); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) < 1+voteMinOptions {
		return []string{voteUsage}, nil
	}

	question := helpers.ClipRunes(parts[0], voteQuestionRune)
	options := parts[1:]
	if len(options) > voteMaxOptions() {
		return []string{fmt.Sprintf("Максимум %d вариантов", voteMaxOptions())}, nil
	}
	for i := range options {
		options[i] = helpers.ClipRunes(options[i], voteLabelRune)
	}

	pollMu.Lock()
	defer pollMu.Unlock()

	if polls[scope] != nil {
		return []string{"Опрос уже идёт. .vote status"}, nil
	}

	pollToken++
	tok := pollToken
	ttl := voteRoundDur()
	p := &poll{
		question: question,
		options:  options,
		votes:    map[string]int{},
		deadline: time.Now().Add(ttl),
		token:    tok,
	}
	p.timer = time.AfterFunc(ttl, func() { voteOnTimeout(scope, tok) })
	polls[scope] = p

	slog.Info("vote started", "scope", scope, "options", len(options))

	var b strings.Builder
	fmt.Fprintf(&b, "📊 Опрос: %s", question)
	for i, opt := range options {
		fmt.Fprintf(&b, "\n.vote %d - %s", i+1, opt)
	}
	return []string{b.String()}, nil
}

// voteCast records the sender's single vote, replacing any previous choice.
func voteCast(scope, channelName, senderKey, arg string) ([]string, error) {
	if strings.TrimSpace(channelName) == "" {
		return []string{voteChannelOnly}, nil
	}
	n, err := strconv.Atoi(arg)
	if err != nil {
		return []string{voteUsage}, nil
	}

	pollMu.Lock()
	defer pollMu.Unlock()

	p := polls[scope]
	if p == nil {
		return []string{voteNoPoll}, nil
	}
	if n < 1 || n > len(p.options) {
		return []string{fmt.Sprintf("Вариант 1–%d", len(p.options))}, nil
	}

	p.votes[senderKey] = n - 1
	return []string{fmt.Sprintf("✅ Голос за: %s", p.options[n-1])}, nil
}

// voteStatus shows the current results plus the remaining time.
func voteStatus(scope, channelName string) ([]string, error) {
	if strings.TrimSpace(channelName) == "" {
		return []string{voteChannelOnly}, nil
	}

	pollMu.Lock()
	defer pollMu.Unlock()

	p := polls[scope]
	if p == nil {
		return []string{voteNoPoll}, nil
	}

	left := time.Until(p.deadline)
	if left < 0 {
		left = 0
	}
	m := int(left / time.Minute)
	s := int(left/time.Second) % 60
	return []string{fmt.Sprintf("⏱%d:%02d\n%s", m, s, voteResultLocked(p))}, nil
}

// voteEnd closes the poll early and returns the final results. Anyone may end it.
func voteEnd(scope, channelName string) ([]string, error) {
	if strings.TrimSpace(channelName) == "" {
		return []string{voteChannelOnly}, nil
	}

	pollMu.Lock()
	defer pollMu.Unlock()

	p := polls[scope]
	if p == nil {
		return []string{voteNoPoll}, nil
	}
	p.timer.Stop()
	res := voteResultLocked(p)
	delete(polls, scope)

	slog.Info("vote ended", "scope", scope, "votes", len(p.votes))
	return []string{res}, nil
}

// voteOnTimeout is the AfterFunc callback. It bails on a stale fire (the poll was
// ended/restarted, so it is gone or carries a different token), finalizes under
// the lock, then announces the results after releasing pollMu — Announce resolves
// the scope and enqueues over BLE, which must not run while holding the lock.
func voteOnTimeout(scope string, token uint64) {
	pollMu.Lock()
	p := polls[scope]
	if p == nil || p.token != token {
		pollMu.Unlock()
		return
	}
	res := voteResultLocked(p)
	delete(polls, scope)
	pollMu.Unlock()

	slog.Info("vote timed out", "scope", scope, "votes", len(p.votes))
	games.Announce(scope, []string{res})
}

// voteResultLocked renders the full numbered results list. The caller must hold
// pollMu. The list can exceed the mesh byte limit with many options; the send
// layer's shrink.Fit splits it across messages.
func voteResultLocked(p *poll) string {
	counts := make([]int, len(p.options))
	for _, idx := range p.votes {
		if idx >= 0 && idx < len(counts) {
			counts[idx]++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📊 Итоги: %s", p.question)
	for i, opt := range p.options {
		fmt.Fprintf(&b, "\n%d) %s: %d", i+1, opt, counts[i])
	}
	return b.String()
}
