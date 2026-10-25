package companion

import (
	"sync"
	"time"
)

// maxInboxSize caps the unread queue. When the cap is exceeded the OLDEST
// messages are dropped (with a log by the caller-visible counter), so a client
// that never polls cannot grow the queue without bound.
const maxInboxSize = 1000

// Message is one received mesh message, normalized for consumers (HTTP JSON,
// voice tools). DM and channel messages share the shape: the dm/channel
// discriminant is Type, and the fields that don't apply stay at their zero
// value ("" / -1).
type Message struct {
	ID           int64     `json:"id"`
	Type         string    `json:"type"` // "dm" | "channel"
	Sender       string    `json:"sender"`
	SenderKey    string    `json:"sender_key"`    // full hex pubkey, dm only
	Channel      string    `json:"channel"`       // channel name, channel msgs only
	ChannelIndex int       `json:"channel_index"` // -1 for dm
	Text         string    `json:"text"`
	Timestamp    time.Time `json:"timestamp"`   // mesh sender-side timestamp
	ReceivedAt   time.Time `json:"received_at"` // server receive time
}

// Inbox is a bounded FIFO of unread messages safe for concurrent use. Producers
// (the live forwarder, the startup device sync) call Add; GET /get calls Drain.
type Inbox struct {
	mu      sync.Mutex
	msgs    []Message
	lastID  int64 // highest ID assigned so far (0 when none)
	dropped int64 // total messages dropped due to the cap, for logging
}

// Add appends msg, assigning it the next sequential ID and ReceivedAt (when
// zero). It returns the stored message.
func (in *Inbox) Add(msg Message) Message {
	in.mu.Lock()
	defer in.mu.Unlock()

	if msg.ReceivedAt.IsZero() {
		msg.ReceivedAt = time.Now()
	}
	in.lastID++
	msg.ID = in.lastID

	if len(in.msgs) >= maxInboxSize {
		overflow := len(in.msgs) - maxInboxSize + 1
		in.msgs = append(in.msgs[:0], in.msgs[overflow:]...)
		in.dropped += int64(overflow)
	}
	in.msgs = append(in.msgs, msg)
	return msg
}

// Drain returns all pending messages in arrival order and clears the inbox.
// The result is never nil, so JSON encoding always yields an array.
func (in *Inbox) Drain() []Message {
	in.mu.Lock()
	defer in.mu.Unlock()

	msgs := in.msgs
	in.msgs = nil
	if msgs == nil {
		msgs = []Message{}
	}
	return msgs
}

// Len returns the number of pending messages.
func (in *Inbox) Len() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.msgs)
}

// Since returns the pending messages with ID greater than after, in arrival
// order, without removing them. It backs the cursor-based GET /messages
// endpoint, where the client tracks the last ID it has seen. The result is
// never nil.
func (in *Inbox) Since(after int64) []Message {
	in.mu.Lock()
	defer in.mu.Unlock()

	var out []Message
	for _, m := range in.msgs {
		if m.ID > after {
			out = append(out, m)
		}
	}
	if out == nil {
		out = []Message{}
	}
	return out
}

// LastID returns the highest message ID assigned so far (0 when none). A
// client can use it as a cursor: everything newer than it arrives later.
func (in *Inbox) LastID() int64 {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.lastID
}
