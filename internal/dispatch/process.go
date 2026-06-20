package dispatch

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"meshbot/internal/commands/advert"
	"meshbot/internal/registry"
	"meshbot/internal/config"
	"meshbot/internal/meshcore"
	"meshbot/internal/shrink"
)

// inflightSet tracks senders that currently have a command being processed, so
// one sender can't occupy multiple worker slots by spamming slow commands.
// begin reports whether the slot was acquired; false means the sender already
// has a command in flight.
type inflightSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func (s *inflightSet) begin(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; ok {
		return false
	}
	s.m[key] = struct{}{}
	return true
}

func (s *inflightSet) done(key string) {
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
}

func (d *Dispatcher) processDM(m *meshcore.Message) {
	senderKeyHex := fmt.Sprintf("%x", m.SenderKey)
	if !config.Get().Security.Direct.IsAllowed(senderKeyHex) {
		log().Info("Direct message filtered by security policy", "sender", senderKeyHex[:12])
		return
	}

	// Look up the full contact record for the sender.
	shortKey := fmt.Sprintf("%x", m.SenderKey[:4])
	contact := d.client.FindContactByKeyPrefix(m.SenderKey[:6])
	if contact == nil {
		contact = &meshcore.Contact{
			PublicKey: m.SenderKey,
			Name:      fmt.Sprintf("?%x", m.SenderKey[:6]),
		}
	}

	log().Info("Direct message received", "sender", contact.Name, "key", shortKey, "text", m.Text)

	// Notify observers (e.g. the migrator) of the received DM. Done before the
	// per-sender in-flight guard so it fires for any DM, not just commands, and
	// never consumes a command slot.
	for _, o := range d.observers {
		o.OnDirectMessage(m, contact)
	}

	// One command in flight per sender: reply busy instead of queuing a second.
	if !d.inflight.begin("dm:" + senderKeyHex) {
		log().Info("Sender busy, replying busy", "sender", contact.Name)
		if !d.TryEnqueue(Outgoing{Contact: contact, Text: "busy, try again", Ack: true}) {
			log().Warn("Outgoing channel full, dropping busy reply", "sender", contact.Name)
		}
		return
	}
	defer d.inflight.done("dm:" + senderKeyHex)

	if flood, ok := advert.ParseAdvertCommand(m.Text); ok {
		log().Info("Sending advert", "flood", flood)
		reply := "success"
		if err := d.client.SendSelfAdvert(flood); err != nil {
			log().Error("SendSelfAdvert failed", "error", err)
			reply = "error: " + err.Error()
		}
		d.Enqueue(Outgoing{Contact: contact, Text: reply, Ack: true})
		return
	}

	pingMeta := buildPingMeta(m.PathLength, m.PathBytes, m.Timestamp, contact.Latitude, contact.Longitude, d.client.GetContacts())
	setSelfCoords(pingMeta, d.client)

	replies, name, err := registry.Dispatch(registry.NewCtx(m.Text, "", string(contact.PublicKey), contact.Name, pingMeta, config.Get()))
	if err != nil {
		log().Error("Command error", "error", err)
		return
	}
	if len(replies) == 0 {
		log().Debug("No reply generated, skipping", "text", m.Text)
		return
	}

	for _, reply := range shrink.Fit(name, "", replies) {
		d.Enqueue(Outgoing{Contact: contact, Text: reply, Ack: true})
	}
}

func (d *Dispatcher) processChannelMessage(m *meshcore.ChannelMessage) {
	log().Info("Channel message received", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)

	channels := d.client.GetChannels()
	channel, ok := channels[m.ChannelIndex]
	if !ok || channel.IsEmpty {
		log().Warn("Channel not found or empty", "channel", m.ChannelIndex)
		return
	}

	if !config.Get().Security.Channels.IsAllowed(channel.Name) {
		log().Info("Channel message filtered by security policy", "channel", channel.Name)
		return
	}

	if d.tgb != nil {
		d.tgb.Forward(channel.Name, m.SenderName, m.Text)
	}

	// Notify observers (e.g. the migrator) of the received channel message. Done
	// before the per-sender in-flight guard so it fires for any message, not just
	// commands, and never consumes a command slot.
	for _, o := range d.observers {
		o.OnChannelMessage(m, channel)
	}

	// Resolve sender coordinates and public key for commands.
	var senderLat, senderLon *float64
	var senderKeyHexCh string
	if c := d.client.FindContactByName(m.SenderName); c != nil {
		senderLat = c.Latitude
		senderLon = c.Longitude
		senderKeyHexCh = fmt.Sprintf("%x", c.PublicKey)
	}

	// forwardBotReply sends a bot reply to Telegram only when this mesh channel
	// has a matching entry in the Targets config (Forward filters internally).
	botName := "Bot"
	if name := d.client.SelfName(); name != "" {
		botName = name
	}
	forwardBotReply := func(text string) {
		if d.tgb != nil && d.tgb.HasTarget(channel.Name) {
			d.tgb.Forward(channel.Name, botName, text)
		}
	}

	// One command in flight per sender: reply busy instead of queuing a second.
	if !d.inflight.begin("ch:" + m.SenderName) {
		log().Info("Sender busy, replying busy", "channel", channel.Name, "sender", m.SenderName)
		text := fmt.Sprintf("@[%s] busy, try again", m.SenderName)
		if d.TryEnqueue(Outgoing{Channel: channel, Text: text, Ack: true}) {
			forwardBotReply(text)
		} else {
			log().Warn("Outgoing channel full, dropping busy reply", "channel", channel.Name)
		}
		return
	}
	defer d.inflight.done("ch:" + m.SenderName)

	if flood, ok := advert.ParseAdvertCommand(m.Text); ok {
		log().Info("Sending advert", "flood", flood)
		reply := "success"
		if err := d.client.SendSelfAdvert(flood); err != nil {
			log().Error("SendSelfAdvert failed", "error", err)
			reply = "error: " + err.Error()
		}
		text := fmt.Sprintf("@[%s] %s", m.SenderName, reply)
		d.Enqueue(Outgoing{Channel: channel, Text: text, Ack: true})
		forwardBotReply(text)
		return
	}

	pingMeta := buildPingMeta(m.PathLength, m.PathBytes, m.Timestamp, senderLat, senderLon, d.client.GetContacts())
	setSelfCoords(pingMeta, d.client)

	replies, name, err := registry.Dispatch(registry.NewCtx(m.Text, channel.Name, senderKeyHexCh, m.SenderName, pingMeta, config.Get()))
	if err != nil {
		log().Error("Command error", "error", err)
		return
	}
	if len(replies) == 0 {
		log().Debug("No reply generated, skipping", "text", m.Text)
		return
	}

	// The "@[sender] " prefix lands on the first wire message only; shrink.Fit
	// reserves room for it and splits/shrinks per the configured variant.
	prefix := fmt.Sprintf("@[%s] ", m.SenderName)
	for i, text := range shrink.Fit(name, prefix, replies) {
		d.Enqueue(Outgoing{Channel: channel, Text: text, Ack: i == 0})
		forwardBotReply(text)
	}
}

// buildPingMeta constructs a PingMeta from raw message fields. It resolves
// intermediate hop coordinates from the contact list when available.
func buildPingMeta(pathLength int, pathBytes []byte, timestamp time.Time, senderLat, senderLon *float64, contacts map[string]*meshcore.Contact) *registry.PingMeta {
	meta := &registry.PingMeta{
		PathLength: pathLength,
		PathBytes:  pathBytes,
		Timestamp:  timestamp,
		SenderLat:  senderLat,
		SenderLon:  senderLon,
	}
	if pathLength > 0 && len(pathBytes) > 0 {
		var bph int
		switch len(pathBytes) {
		case pathLength:
			bph = 1
		case pathLength * 2:
			bph = 2
		}
		if bph > 0 {
			meta.HopCoords, meta.HopKnown = resolveHopCoords(pathBytes, pathLength, bph, contacts)
		}
	}
	return meta
}

// setSelfCoords fills pingMeta.SelfLat/SelfLon from config (when use_config_coords
// is true) or from the device's own SelfInfo. Config coords take priority when
// the flag is set.
func setSelfCoords(pingMeta *registry.PingMeta, client interface {
	GetSelfInfo() *meshcore.SelfInfo
}) {
	pingCfg := config.Get().Trace
	if pingCfg.UseConfigCoords {
		pingMeta.SelfLat = pingCfg.Lat
		pingMeta.SelfLon = pingCfg.Lon
		return
	}
	if self := client.GetSelfInfo(); self != nil {
		pingMeta.SelfLat = self.Latitude
		pingMeta.SelfLon = self.Longitude
	}
}

// resolveHopCoords resolves GPS coordinates for each intermediate hop in
// pathBytes. bytesPerHop is 1 or 2; each hop's bytes are matched against contact
// public key prefixes.
func resolveHopCoords(pathBytes []byte, hopCount, bytesPerHop int, contacts map[string]*meshcore.Contact) (coords [][2]float64, known []bool) {
	coords = make([][2]float64, hopCount)
	known = make([]bool, hopCount)
	for i := 0; i < hopCount; i++ {
		chunk := pathBytes[i*bytesPerHop : (i+1)*bytesPerHop]
		for _, c := range contacts {
			if len(c.PublicKey) >= bytesPerHop && bytes.Equal(c.PublicKey[:bytesPerHop], chunk) {
				if c.Latitude != nil && c.Longitude != nil {
					coords[i] = [2]float64{*c.Latitude, *c.Longitude}
					known[i] = true
				}
				break
			}
		}
	}
	return
}
