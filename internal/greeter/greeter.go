// Package greeter handles two related courtesy behaviours that fire from the
// device's advert/contact stream: greeting newly seen nodes on the public
// channel, and nudging nodes still routing with 1-byte hashes to switch to
// 2-byte. Both emit channel messages through a caller-supplied Sender, so this
// package has no dependency on the outgoing-message pipeline.
package greeter

import (
	"fmt"
	"sync"

	"meshbot/internal/commands"
	"meshbot/internal/logging"
	meshcore "meshbot/internal/meshcore_bt"
)

var log = logging.For("greeter")

// Sender queues a channel message for transmission. ack requests an end-to-end
// acknowledgement. Implementations must not block.
type Sender func(ch *meshcore.Channel, text string, ack bool)

// Greeter greets new nodes and posts 1-byte→2-byte hash migration reminders.
// It is constructed once and Register installs the device observers.
type Greeter struct {
	client   *meshcore.MeshCoreClient
	known    *commands.KnownKeys
	notifier *commands.MigrationNotifier
	send     Sender

	// pendingGreets holds public-key hex strings of nodes whose advert arrived
	// before their contact record was in the cache. They are greeted once the
	// full contact is received via OnContact.
	mu      sync.Mutex
	pending map[string]struct{}
}

// New returns a Greeter. known tracks already-greeted nodes, notifier decides
// when to post migration reminders, and send queues outgoing channel messages.
func New(client *meshcore.MeshCoreClient, known *commands.KnownKeys, notifier *commands.MigrationNotifier, send Sender) *Greeter {
	return &Greeter{
		client:   client,
		known:    known,
		notifier: notifier,
		send:     send,
		pending:  make(map[string]struct{}),
	}
}

// Register installs the advert and contact observers on the client.
func (g *Greeter) Register() {
	g.client.OnAdvert(g.onAdvert)
	g.client.OnContact(g.onContact)
}

// WarnOldHash posts the migration reminder on channel ch for a node still using
// 1-byte routing hashes, subject to the per-node cooldown. hashSize is the
// node's bytes-per-hop (<=0 = unknown, skipped). displayName is the @[…] mention;
// pubKeyHex keys the cooldown.
func (g *Greeter) WarnOldHash(ch *meshcore.Channel, pubKeyHex, displayName string, hashSize int) {
	if ch == nil || ch.IsEmpty {
		return
	}
	if !g.notifier.ShouldWarn(pubKeyHex, hashSize) {
		return
	}
	if displayName == "" {
		displayName = pubKeyHex
		if len(displayName) > 8 {
			displayName = displayName[:8]
		}
	}
	text := fmt.Sprintf("@[%s] %s", displayName, g.notifier.Message())
	log().Info("Queuing 1-byte hash migration warning", "node", displayName, "channel", ch.Name)
	g.send(ch, text, true)
}

func (g *Greeter) onAdvert(contact *meshcore.Contact) {
	// Detect a node still routing with 1-byte hashes. Contact.PathLength is the
	// firmware-encoded out_path byte (0xFF = flood/unknown); only paths with real
	// hops reveal the hash size.
	if pl := contact.PathLength; pl != 0xFF && meshcore.PathHashCount(byte(pl)) > 0 {
		g.WarnOldHash(g.client.GetChannels()[0], fmt.Sprintf("%x", contact.PublicKey),
			contact.Name, meshcore.PathHashSize(byte(pl)))
	}

	alreadyKnown, err := g.known.CheckAndAdd(contact.PublicKey, contact.Name)
	if err != nil {
		log().Error("Failed to update known keys", "error", err)
		return
	}
	if alreadyKnown {
		return
	}
	if contact.Type == 0 && contact.Name == "" {
		// Stub contact: type/name unknown yet. Request a contacts refresh and
		// defer the greeting until the full record arrives via OnContact.
		keyHex := fmt.Sprintf("%x", contact.PublicKey)
		log().Info("New node seen (stub), deferring greeting", "pub_key", keyHex)
		g.mu.Lock()
		g.pending[keyHex] = struct{}{}
		g.mu.Unlock()
		if err := g.client.RequestContacts(0); err != nil {
			log().Error("Failed to request contacts after stub advert", "error", err)
		}
		return
	}
	g.greet(contact)
}

func (g *Greeter) onContact(contact *meshcore.Contact) {
	keyHex := fmt.Sprintf("%x", contact.PublicKey)
	g.mu.Lock()
	_, isPending := g.pending[keyHex]
	if isPending {
		delete(g.pending, keyHex)
	}
	g.mu.Unlock()
	if !isPending {
		return
	}
	// This contact arrived as a stub advert earlier; greet it now that we have
	// its full record (name, type). The key was already recorded as known in
	// onAdvert, so no second CheckAndAdd is needed.
	g.greet(contact)
}

// greet posts the welcome message for a node on public channel 0.
func (g *Greeter) greet(contact *meshcore.Contact) {
	pubCh := g.client.GetChannels()[0]
	if pubCh == nil || pubCh.IsEmpty {
		log().Warn("Public channel 0 not available, skipping greeting")
		return
	}
	msg := commands.GreetMessage(contact.Name, contact.PublicKey, contact.Type)
	log().Info("New node seen, queuing greeting", "name", contact.Name, "type", contact.Type)
	g.send(pubCh, msg, true)
}
