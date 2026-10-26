// Package migrator nudges mesh nodes still routing with 1-byte hashes to switch
// to 2-byte. It observes received messages through the dispatcher and replies in
// kind — a channel warning for a channel message, a direct reply for a DM —
// using the dispatcher purely for delivery. The hash size is read from each
// message's real path (bytes-per-hop), never from stale advert routing, since
// adverts don't carry the node's current routing hash size.
package migrator

import (
	"fmt"

	"github.com/stempit/meshcore-go-bot/internal/logging"
	"github.com/stempit/meshcore-go"
)

var log = logging.For("migrator")

// ChannelSender queues a channel message for transmission. ack requests an
// end-to-end acknowledgement. Implementations must not block.
type ChannelSender func(ch *meshcore.Channel, text string, ack bool)

// DMSender queues a direct message to a contact. ack requests an end-to-end
// acknowledgement. Implementations must not block.
type DMSender func(contact *meshcore.Contact, text string, ack bool)

// Migrator detects 1-byte routing on received messages and queues a migration
// reminder, subject to the per-node cooldown held by Notifier. It implements the
// dispatcher's incoming-message Observer.
type Migrator struct {
	client   *meshcore.MeshCoreClient
	notifier *Notifier
	sendCh   ChannelSender
	sendDM   DMSender
}

// New returns a Migrator. client resolves channel-message senders to their
// public key (the cooldown key); sendCh and sendDM deliver the reminders.
func New(notifier *Notifier, client *meshcore.MeshCoreClient, sendCh ChannelSender, sendDM DMSender) *Migrator {
	return &Migrator{client: client, notifier: notifier, sendCh: sendCh, sendDM: sendDM}
}

// OnDirectMessage observes a received DM. A 1-byte path is answered with a
// direct reply, so the nudge reaches the sender the same way the message came.
func (m *Migrator) OnDirectMessage(msg *meshcore.Message, contact *meshcore.Contact) {
	size, ok := hashSize(msg.PathLength, msg.PathBytes)
	if !ok {
		return
	}
	key := fmt.Sprintf("%x", msg.SenderKey)
	if text, ok := m.warnText(key, contact.Name, size); ok {
		log().Info("Queuing 1-byte hash migration warning (DM)", "node", contact.Name)
		m.sendDM(contact, text, true)
	}
}

// OnChannelMessage observes a received channel message. A 1-byte path is
// answered on the same channel.
func (m *Migrator) OnChannelMessage(msg *meshcore.ChannelMessage, channel *meshcore.Channel) {
	size, ok := hashSize(msg.PathLength, msg.PathBytes)
	if !ok {
		return
	}
	// Prefer the sender's public key for the cooldown; fall back to the name when
	// the contact isn't known yet.
	key := "name:" + msg.SenderName
	if c := m.client.FindContactByName(msg.SenderName); c != nil {
		key = fmt.Sprintf("%x", c.PublicKey)
	}
	if text, ok := m.warnText(key, msg.SenderName, size); ok {
		log().Info("Queuing 1-byte hash migration warning", "node", msg.SenderName, "channel", channel.Name)
		m.sendCh(channel, text, true)
	}
}

// warnText runs the per-node cooldown check and, when the node should be nudged,
// builds the reminder text addressed to displayName. ok is false when the node
// must not be warned (disabled, not 1-byte, or within cooldown).
func (m *Migrator) warnText(pubKeyHex, displayName string, hashSize int) (string, bool) {
	if !m.notifier.ShouldWarn(pubKeyHex, hashSize) {
		return "", false
	}
	if displayName == "" {
		displayName = pubKeyHex
		if len(displayName) > 8 {
			displayName = displayName[:8]
		}
	}
	return fmt.Sprintf("@[%s] %s", displayName, m.notifier.Message()), true
}

// hashSize derives a node's routing bytes-per-hop from a message's real path:
// the meaningful path bytes divided by the hop count. ok is false when the path
// carries no usable hops (direct/flood, or a length that doesn't divide evenly).
func hashSize(hops int, pathBytes []byte) (int, bool) {
	if hops <= 0 || len(pathBytes) == 0 || len(pathBytes)%hops != 0 {
		return 0, false
	}
	return len(pathBytes) / hops, true
}
