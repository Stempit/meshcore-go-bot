// Package dispatch is the bot's message engine. It owns both directions of the
// mesh-radio message flow: an inbound worker pool that routes received DMs and
// channel messages to command handlers, and a serialized outbound sender that
// paces and retries transmissions (the radio can only transmit one message at a
// time). Subsystems are wired in by the composition root in cmd/bot.
package dispatch

import (
	"context"
	"errors"
	"strings"
	"time"

	"meshbot/internal/config"
	"meshbot/internal/logging"
	"meshbot/internal/meshcore"
	"meshbot/internal/shrink"
	"meshbot/internal/bridge/tgbridge"
)

var log = logging.For("bot")

// Outgoing is a reply to be sent back over the mesh radio. Exactly one of
// Contact (DM) or Channel (channel reply) must be non-nil.
type Outgoing struct {
	Contact *meshcore.Contact
	Channel *meshcore.Channel
	Text    string
	Ack     bool
}

// Observer reacts to received messages before command processing — used by
// subsystems that watch traffic rather than answer commands (e.g. the migrator).
// The dispatcher resolves the contact (DM) or channel before notifying, so
// observers don't repeat the lookup. Implementations must not block.
type Observer interface {
	OnDirectMessage(*meshcore.Message, *meshcore.Contact)
	OnChannelMessage(*meshcore.ChannelMessage, *meshcore.Channel)
}

// Dispatcher routes incoming mesh messages to command handlers and serializes
// outgoing replies. Construct with New, wire optional subsystems with the
// setters, then call Run.
type Dispatcher struct {
	client    *meshcore.MeshCoreClient
	tgb       *tgbridge.Bridge
	observers []Observer
	inflight  *inflightSet

	incoming chan any
	outgoing chan Outgoing
}

// New returns a Dispatcher bound to client with buffered message channels.
func New(client *meshcore.MeshCoreClient) *Dispatcher {
	return &Dispatcher{
		client:   client,
		inflight: &inflightSet{m: make(map[string]struct{})},
		incoming: make(chan any, 64),
		outgoing: make(chan Outgoing, 64),
	}
}

// SetTelegram attaches a Telegram bridge for forwarding channel traffic. nil
// (the default) disables forwarding.
func (d *Dispatcher) SetTelegram(tgb *tgbridge.Bridge) { d.tgb = tgb }

// AddObserver registers o to be notified of received messages. Call before Run.
func (d *Dispatcher) AddObserver(o Observer) { d.observers = append(d.observers, o) }

// Enqueue queues a reply for transmission. It is safe for concurrent use.
func (d *Dispatcher) Enqueue(msg Outgoing) { d.outgoing <- msg }

// TryEnqueue queues a reply without blocking, reporting whether it was accepted.
// Use it on hot paths where a full queue should drop the message rather than
// stall the caller.
func (d *Dispatcher) TryEnqueue(msg Outgoing) bool {
	select {
	case d.outgoing <- msg:
		return true
	default:
		return false
	}
}

// Submit feeds an incoming message (*meshcore.Message or *meshcore.ChannelMessage)
// into the processing pipeline. Stored and live messages both flow through here.
func (d *Dispatcher) Submit(msg any) { d.incoming <- msg }

// Run starts the inbound worker pool and the outbound sender goroutines. They
// run for the lifetime of the process; Run returns immediately.
func (d *Dispatcher) Run(ctx context.Context) {
	go d.runSender()
	go d.runWorkers()
}

// runSender drains the outgoing queue, transmitting one message at a time with
// the pacing the radio needs to switch RX→TX and avoid back-to-back sends.
func (d *Dispatcher) runSender() {
	// Set lastSent to now so the first send always waits the full gap.
	lastSent := time.Now()
	for msg := range d.outgoing {
		// Always wait at least 1s before sending — gives the device time to
		// switch from RX to TX mode after receiving the incoming message.
		time.Sleep(1 * time.Second)
		// Enforce a minimum gap between consecutive radio sends.
		if wait := 5*time.Second - time.Since(lastSent); wait > 0 {
			time.Sleep(wait)
		}
		if msg.Channel != nil {
			transmissions := config.Get().Connection.ChannelTransmissions
			if transmissions < 1 {
				transmissions = 3 // default: up to 3 transmissions, stops early on mesh repeat
			}
			if err := d.client.SendChannelMessageWithRetry(msg.Channel, msg.Text, msg.Ack, 1, transmissions, 3*time.Second); err != nil {
				log().Error("Failed to send channel reply", "channel", msg.Channel.Name, "error", err)
			} else {
				log().Info("Channel reply sent", "channel", msg.Channel.Name)
			}
		} else if msg.Contact != nil {
			err := d.client.SendMessageWithRetry(msg.Contact, msg.Text, true, 3, 3, 3*time.Second)
			if err != nil && !errors.Is(err, meshcore.ErrDeviceRejected) {
				time.Sleep(5 * time.Second)
				err = d.client.SendMessageWithRetry(msg.Contact, msg.Text, false, 3, 3, 3*time.Second)
			}
			if err != nil {
				log().Error("Failed to send DM reply", "error", err)
			} else {
				log().Info("DM reply sent")
			}
		}
		lastSent = time.Now()
	}
}

// runWorkers processes incoming messages concurrently so slow commands (.claw,
// .llm — they block on network) don't hold up fast ones like .ping. Reply
// generation runs concurrently; reply sending stays serialized through the
// outgoing queue. The semaphore caps total in-flight messages so a burst can't
// spawn unbounded goroutines. The per-sender guard (one command in flight per
// sender) lives inside the processors, where the sender identity is resolved.
func (d *Dispatcher) runWorkers() {
	const maxConcurrent = 8
	sem := make(chan struct{}, maxConcurrent)
	for msg := range d.incoming {
		sem <- struct{}{}
		go func(msg any) {
			defer func() { <-sem }()
			switch m := msg.(type) {
			case *meshcore.Message:
				d.processDM(m)
			case *meshcore.ChannelMessage:
				d.processChannelMessage(m)
			}
		}(msg)
	}
}

// SendChannelMessage queues text on channel ch, dropping it if the outgoing
// queue is full. It is a delivery callback for subsystems (greeter, migrator)
// that emit channel messages without depending on the dispatcher's types.
func (d *Dispatcher) SendChannelMessage(ch *meshcore.Channel, text string, ack bool) {
	if !d.TryEnqueue(Outgoing{Channel: ch, Text: text, Ack: ack}) {
		log().Warn("Outgoing queue full, dropping channel message", "channel", ch.Name)
	}
}

// SendDirectMessage queues text to contact, dropping it if the outgoing queue is
// full. It is a delivery callback for subsystems that reply directly.
func (d *Dispatcher) SendDirectMessage(contact *meshcore.Contact, text string, ack bool) {
	if !d.TryEnqueue(Outgoing{Contact: contact, Text: text, Ack: ack}) {
		log().Warn("Outgoing queue full, dropping direct message", "node", contact.Name)
	}
}

// AnnounceToScope resolves a games scope string to a channel or contact and
// queues each message, mesh-fitting via the "words" variant. It is the games
// Announcer: the timer-driven results path that has no incoming Outgoing target.
// Delivery is best-effort — a full outgoing queue drops the message (logged)
// rather than blocking the timer goroutine.
//
// DM scopes carry the sender's raw public-key bytes ("dm:" + string(pubkey), as
// set by gameScope in processDM); channel scopes are the channel name.
func (d *Dispatcher) AnnounceToScope(scope string, msgs []string) {
	if rest, ok := strings.CutPrefix(scope, "dm:"); ok {
		key := []byte(rest)
		c := d.client.FindContactByKeyPrefix(key[:min(6, len(key))])
		if c == nil {
			log().Warn("Announce: contact not found for scope")
			return
		}
		for _, m := range shrink.Fit("words", "", msgs) {
			d.SendDirectMessage(c, m, true)
		}
		return
	}
	ch := d.client.GetChannelByName(scope)
	if ch == nil {
		log().Warn("Announce: channel not found", "scope", scope)
		return
	}
	for _, m := range shrink.Fit("words", "", msgs) {
		d.SendChannelMessage(ch, m, false)
	}
}
