// Package greeter greets newly seen nodes on the public channel as they appear
// in the device's advert/contact stream. It emits channel messages through a
// caller-supplied Sender, so this package has no dependency on the
// outgoing-message pipeline.
package greeter

import (
	"fmt"
	"sync"

	"meshbot/internal/logging"
	"meshbot/internal/meshcore"
)

var log = logging.For("greeter")

// Sender queues a channel message for transmission. ack requests an end-to-end
// acknowledgement. Implementations must not block.
type Sender func(ch *meshcore.Channel, text string, ack bool)

// Greeter greets new nodes on the public channel. It is constructed once and
// Register installs the device observers.
type Greeter struct {
	client *meshcore.MeshCoreClient
	known  *KnownKeys
	send   Sender

	// pendingGreets holds public-key hex strings of nodes whose advert arrived
	// before their contact record was in the cache. They are greeted once the
	// full contact is received via OnContact.
	mu      sync.Mutex
	pending map[string]struct{}
}

// New returns a Greeter. known tracks already-greeted nodes and send queues
// outgoing channel messages.
func New(client *meshcore.MeshCoreClient, known *KnownKeys, send Sender) *Greeter {
	return &Greeter{
		client:  client,
		known:   known,
		send:    send,
		pending: make(map[string]struct{}),
	}
}

// Register installs the advert and contact observers on the client.
func (g *Greeter) Register() {
	g.client.OnAdvert(g.onAdvert)
	g.client.OnContact(g.onContact)
}

func (g *Greeter) onAdvert(contact *meshcore.Contact) {
	// NOTE: 1-byte hash migration warnings are deliberately NOT raised from
	// adverts (that lives in the migrator package, off received messages). The
	// advert push frame carries only the public key, so the only hash-size signal
	// here is the cached contact's out_path_len — but that is OUR outbound route
	// to the node, not the node's advert hash size, and the firmware never
	// refreshes it on advert reception, so it produces false warnings.

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
	msg := GreetMessage(contact.Name, contact.PublicKey, contact.Type)
	log().Info("New node seen, queuing greeting", "name", contact.Name, "type", contact.Type)
	g.send(pubCh, msg, true)
}
