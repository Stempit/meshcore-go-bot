// Package dispatch is the bot's message engine. It owns both directions of the
// mesh-radio message flow: an inbound worker pool that routes received DMs and
// channel messages to command handlers, and a serialized outbound sender that
// paces and retries transmissions (the radio can only transmit one message at a
// time). Subsystems are wired in by the composition root in cmd/bot.
package dispatch

import (
	"context"
	"errors"
	"time"

	"meshbot/internal/config"
	"meshbot/internal/greeter"
	"meshbot/internal/logging"
	meshcore "meshbot/internal/meshcore_bt"
	"meshbot/internal/tgbridge"
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

// Dispatcher routes incoming mesh messages to command handlers and serializes
// outgoing replies. Construct with New, wire optional subsystems with the
// setters, then call Run.
type Dispatcher struct {
	client   *meshcore.MeshCoreClient
	tgb      *tgbridge.Bridge
	greeter  *greeter.Greeter
	inflight *inflightSet

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

// SetGreeter attaches the greeter so channel messages can trigger 1-byte hash
// migration warnings.
func (d *Dispatcher) SetGreeter(g *greeter.Greeter) { d.greeter = g }

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

// enqueueChannel adapts Enqueue to the greeter.Sender signature.
func (d *Dispatcher) enqueueChannel(ch *meshcore.Channel, text string, ack bool) {
	if !d.TryEnqueue(Outgoing{Channel: ch, Text: text, Ack: ack}) {
		log().Warn("Outgoing channel full, dropping greeter message", "channel", ch.Name)
	}
}

// GreeterSender returns a greeter.Sender backed by this dispatcher's outgoing
// queue, for use when constructing the greeter.
func (d *Dispatcher) GreeterSender() greeter.Sender { return d.enqueueChannel }
