package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"meshbot/internal/commands"
	"meshbot/internal/config"
	"meshbot/internal/historylog"
	"meshbot/internal/logging"
	meshcore "meshbot/internal/meshcore_bt"
	"meshbot/internal/mqttbridge"
	"meshbot/internal/proxy"
	"meshbot/internal/shrink"
	"meshbot/internal/tgbridge"
)

var log = logging.For("bot")

// processStoredMessages controls whether messages stored on the device before
// bot startup are fed into the processing pipeline. Set to false to skip them.
const processStoredMessages = true

// outgoingMsg is a reply to be sent back over the mesh radio.
// Exactly one of contact (DM) or channel (channel reply) must be non-nil.
type outgoingMsg struct {
	contact *meshcore.Contact
	channel *meshcore.Channel
	text    string
	ack     bool
}

// migrateNotifier nudges nodes still using 1-byte routing hashes to switch to
// 2-byte. It is set once in main and read from the advert observer and the
// channel-message handler. nil until main initializes it.
var migrateNotifier *commands.MigrationNotifier

// warnOldHash posts the migration reminder on channel ch for a node still using
// 1-byte routing hashes, subject to the per-node cooldown. hashSize is the
// node's bytes-per-hop (<=0 = unknown, skipped). displayName is the @[…] mention;
// pubKeyHex keys the cooldown. It is a no-op until migrateNotifier is set.
func warnOldHash(outgoingCh chan<- outgoingMsg, ch *meshcore.Channel, pubKeyHex, displayName string, hashSize int) {
	if migrateNotifier == nil || ch == nil || ch.IsEmpty {
		return
	}
	if !migrateNotifier.ShouldWarn(pubKeyHex, hashSize) {
		return
	}
	if displayName == "" {
		displayName = pubKeyHex
		if len(displayName) > 8 {
			displayName = displayName[:8]
		}
	}
	text := fmt.Sprintf("@[%s] %s", displayName, migrateNotifier.Message())
	log().Info("Queuing 1-byte hash migration warning", "node", displayName, "channel", ch.Name)
	select {
	case outgoingCh <- outgoingMsg{channel: ch, text: text, ack: true}:
	default:
		log().Warn("Outgoing channel full, dropping migration warning", "node", displayName)
	}
}

func main() {
	// config
	if err := config.Load("config.yaml"); err != nil {
		log().Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	shrink.ValidateConfig()

	proxyCfg := config.Get().Proxy
	if err := proxy.Init(proxyCfg.Enabled, proxyCfg.URL); err != nil {
		log().Error("Failed to initialize proxy", "error", err)
		os.Exit(1)
	}
	if proxyCfg.Enabled {
		log().Info("Proxy enabled", "url", proxyCfg.URL)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// history logger
	histlog := historylog.New("log/history")
	defer histlog.Close()

	// meshcore client — BLE (default) or USB serial depending on config
	var client *meshcore.MeshCoreClient
	conn := config.Get().Connection
	switch conn.Type {
	case "serial":
		port := conn.SerialPort
		if port == "" {
			log().Error("Connection.serial_port must be set when Connection.type is \"serial\"")
			os.Exit(1)
		}
		baud := conn.SerialBaud
		if baud == 0 {
			baud = 115200
		}
		client = meshcore.CreateSerialClient(port, baud)
	default:
		client = meshcore.CreateClient()
	}
	defer client.Disconnect()

	// log history in client
	client.SetHistoryLogger(histlog)

	// mqtt init — optional. The bridge is constructed here, in the composition
	// root, and attached to the client as a packet publisher; the meshcore_bt
	// driver itself has no dependency on the MQTT package.
	mqttCfg := config.Get().MQTT
	if bridge := startMQTTBridge(client, &mqttCfg); bridge != nil {
		defer bridge.Stop()
	}
	client.RequestAllStats()

	// ─── Message channels ─────────────────────────────────────────────────────

	// incomingCh carries all messages (DMs and channel messages) for processing.
	// Stored on-device messages and live arrivals both flow through this channel.
	incomingCh := make(chan any, 64)

	// outgoingCh carries replies to be serialized and sent over the radio.
	outgoingCh := make(chan outgoingMsg, 64)

	// ─── Outgoing sender goroutine ────────────────────────────────────────────

	go func() {
		// Set lastSent to now so the first send always waits the full gap.
		lastSent := time.Now()
		for msg := range outgoingCh {
			// Always wait at least 1s before sending — gives the device time to
			// switch from RX to TX mode after receiving the incoming message.
			time.Sleep(1 * time.Second)
			// Enforce a minimum gap between consecutive radio sends.
			if wait := 5*time.Second - time.Since(lastSent); wait > 0 {
				time.Sleep(wait)
			}
			if msg.channel != nil {
				transmissions := config.Get().Connection.ChannelTransmissions
				if transmissions < 1 {
					transmissions = 3 // default: up to 3 transmissions, stops early on mesh repeat
				}
				if err := client.SendChannelMessageWithRetry(msg.channel, msg.text, msg.ack, 1, transmissions, 3*time.Second); err != nil {
					log().Error("Failed to send channel reply", "channel", msg.channel.Name, "error", err)
				} else {
					log().Info("Channel reply sent", "channel", msg.channel.Name)
				}
			} else if msg.contact != nil {
				err := client.SendMessageWithRetry(msg.contact, msg.text, true, 3, 3, 3*time.Second)
				if err != nil && !errors.Is(err, meshcore.ErrDeviceRejected) {
					time.Sleep(5 * time.Second)
					err = client.SendMessageWithRetry(msg.contact, msg.text, false, 3, 3, 3*time.Second)
				}
				if err != nil {
					log().Error("Failed to send DM reply", "error", err)
				} else {
					log().Info("DM reply sent")
				}
			}
			lastSent = time.Now()
		}
	}()

	// ─── Greet new nodes ──────────────────────────────────────────────────────

	knownKeys, err := commands.NewKnownKeys("log/known.txt")
	if err != nil {
		log().Error("Failed to load known keys", "error", err)
		os.Exit(1)
	}

	if err := commands.InitTicketStore("log/tickets.txt"); err != nil {
		log().Error("Failed to load ticket store", "error", err)
		os.Exit(1)
	}

	sendGreeting := func(contact *meshcore.Contact) {
		pubCh := client.GetChannels()[0]
		if pubCh == nil || pubCh.IsEmpty {
			log().Warn("Public channel 0 not available, skipping greeting")
			return
		}
		msg := commands.GreetMessage(contact.Name, contact.PublicKey, contact.Type)
		log().Info("New node seen, queuing greeting", "name", contact.Name, "type", contact.Type)
		select {
		case outgoingCh <- outgoingMsg{channel: pubCh, text: msg, ack: true}:
		default:
			log().Warn("Outgoing channel full, dropping greeting", "name", contact.Name)
		}
	}

	// ─── 1-byte → 2-byte hash migration nudge ────────────────────────────────

	migCfg := config.Get().Migration
	migMsg := migCfg.Message
	if migMsg == "" {
		migMsg = "switch to 2-byte please"
	}
	migCooldown := time.Duration(migCfg.CooldownSec) * time.Second
	if migCooldown <= 0 {
		migCooldown = 24 * time.Hour
	}
	migrateNotifier = commands.NewMigrationNotifier(migCfg.Enabled, migMsg, migCooldown)

	// pendingGreets holds public-key hex strings of nodes whose advert arrived
	// before their contact record was in the cache. They will be greeted once
	// the full contact is received via OnContact.
	pendingGreets := make(map[string]struct{})
	var pendingMu sync.Mutex

	client.OnAdvert(func(contact *meshcore.Contact) {
		// Detect a node still routing with 1-byte hashes. Contact.PathLength is
		// the firmware-encoded out_path byte (0xFF = flood/unknown); only paths
		// with real hops reveal the hash size.
		if pl := contact.PathLength; pl != 0xFF && meshcore.PathHashCount(byte(pl)) > 0 {
			warnOldHash(outgoingCh, client.GetChannels()[0], fmt.Sprintf("%x", contact.PublicKey),
				contact.Name, meshcore.PathHashSize(byte(pl)))
		}

		alreadyKnown, err := knownKeys.CheckAndAdd(contact.PublicKey, contact.Name)
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
			pendingMu.Lock()
			pendingGreets[keyHex] = struct{}{}
			pendingMu.Unlock()
			if err := client.RequestContacts(0); err != nil {
				log().Error("Failed to request contacts after stub advert", "error", err)
			}
			return
		}
		sendGreeting(contact)
	})

	client.OnContact(func(contact *meshcore.Contact) {
		keyHex := fmt.Sprintf("%x", contact.PublicKey)
		pendingMu.Lock()
		_, isPending := pendingGreets[keyHex]
		if isPending {
			delete(pendingGreets, keyHex)
		}
		pendingMu.Unlock()
		if !isPending {
			return
		}
		// This contact arrived as a stub advert earlier; greet it now that we
		// have its full record (name, type). The key was already recorded as
		// known in OnAdvert, so no second CheckAndAdd is needed.
		sendGreeting(contact)
	})

	// ─── Telegram bridge ──────────────────────────────────────────────────────

	var tgb *tgbridge.Bridge
	if tgCfg := config.Get().Telegram; tgCfg.Enabled {
		tgb, err = tgbridge.New(tgCfg, slog.Default(), proxy.HTTPClient())
		if err != nil {
			log().Error("Failed to start Telegram bridge", "error", err)
		} else {
			tgb.OnMessage(func(meshChannelName, senderName, text string) {
				tgChannel := client.GetChannelByName(meshChannelName)
				if tgChannel == nil {
					log().Warn("Telegram bridge: meshcore channel not found", "name", meshChannelName)
					return
				}
				// The "[TG:sender] " prefix lands on the first wire message only;
				// the "telegram" variant (config) decides split vs llm shrink.
				prefix := fmt.Sprintf("[TG:%s] ", senderName)
				for _, part := range shrink.Fit("telegram", prefix, []string{text}) {
					outgoingCh <- outgoingMsg{channel: tgChannel, text: part, ack: false}
				}
			})
			go tgb.Start()
			defer tgb.Stop()
		}
	}

	// ─── Incoming processor goroutine ─────────────────────────────────────────

	// Each incoming message is processed in its own goroutine so that slow
	// commands (.claw, .llm — they block on network) don't hold up fast ones
	// like .ping. Reply *generation* runs concurrently; reply *sending* stays
	// serialized through outgoingCh, since the radio can only transmit one
	// message at a time. The semaphore caps total in-flight messages so a
	// burst can't spawn unbounded goroutines. The per-sender guard (one
	// command in flight per sender) lives inside the processors, where the
	// sender identity is already resolved.
	go func() {
		const maxConcurrent = 8
		sem := make(chan struct{}, maxConcurrent)
		for msg := range incomingCh {
			sem <- struct{}{}
			go func(msg any) {
				defer func() { <-sem }()
				switch m := msg.(type) {
				case *meshcore.Message:
					processDM(m, client, outgoingCh)
				case *meshcore.ChannelMessage:
					processChannelMessage(m, client, tgb, outgoingCh)
				}
			}(msg)
		}
	}()

	// ─── Sync stored messages ─────────────────────────────────────────────────
	// The BLE driver pushes every received message into client.IncomingMessages()
	// AND into the accumulator maps (GetAllMessages / GetAllChannelMessages).
	// We wait for sync to finish, then drain the client channel (so the live
	// forwarder only sees truly new arrivals), log what was stored, and
	// optionally re-queue stored messages through our own pipeline once.

	syncDone := make(chan struct{})
	client.OnNoMoreMessages(func() { close(syncDone) })
	if err := client.SyncNextMessage(); err != nil {
		log().Error("Failed to sync stored messages", "error", err)
		close(syncDone)
	}

	select {
	case <-syncDone:
		log().Info("Device sync complete")
	case <-time.After(15 * time.Second):
		log().Warn("Device sync timed out, proceeding with messages received so far")
	}
	// Replace callback so late "no more messages" responses during live
	// operation don't panic on the already-closed syncDone channel.
	client.OnNoMoreMessages(func() {})

	// Give the BLE device time to settle after the sync loop before we start
	// sending replies. Without this pause the first write times out.
	time.Sleep(2 * time.Second)

	// Drain the client's incoming channel: messages buffered there during sync
	// are already captured in the accumulator maps; we'll re-queue selectively below.
drainLoop:
	for {
		select {
		case <-client.IncomingMessages():
		default:
			break drainLoop
		}
	}

	// Log and optionally process stored messages exactly once.
	for _, m := range client.GetAllMessages() {
		log().Info("Stored DM", "sender", fmt.Sprintf("%x", m.SenderKey[:6]), "text", m.Text)
		if processStoredMessages {
			incomingCh <- m
		}
	}
	for _, m := range client.GetAllChannelMessages() {
		log().Info("Stored channel message", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)
		if processStoredMessages {
			incomingCh <- m
		}
	}

	// ─── Live message forwarder goroutine ─────────────────────────────────────
	// All messages from here on are new live arrivals.

	go func() {
		for msg := range client.IncomingMessages() {
			incomingCh <- msg
		}
	}()

	// ─── Initial stats ────────────────────────────────────────────────────────

	if err := client.RequestAllStats(); err != nil {
		log().Error("Failed to request initial stats", "error", err)
	} else {
		time.Sleep(500 * time.Millisecond)
		fmt.Println(client.FormatStats())
	}

	// ─── Periodic stats (keeps MQTT up to date, detects BLE disconnection) ────

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := client.RequestAllStats(); err != nil {
					log().Error("Failed to request periodic stats", "error", err)
					if !client.IsConnected() {
						log().Warn("BLE connection lost, reconnecting...")
						time.Sleep(5 * time.Second)
						if err := client.Reconnect(); err != nil {
							log().Error("Reconnect failed", "error", err)
						} else {
							log().Info("Reconnected to device")
						}
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	log().Info("Bot running. Press Ctrl+C to exit.")
	<-ctx.Done()
	log().Info("Shutting down...")
}

// startMQTTBridge constructs the MQTT bridge, wires device-info/stats forwarding,
// and attaches it to the client as a packet publisher. MQTT is optional: if the
// bridge fails to start, it logs and returns nil so startup continues without it.
func startMQTTBridge(client *meshcore.MeshCoreClient, cfg *mqttbridge.MQTTConfig) *mqttbridge.MQTTBridge {
	bridge := mqttbridge.NewMQTTBridge(cfg)
	bridge.SetLogger(slog.Default())
	bridge.SetCallbacks(mqttbridge.MQTTCallbacks{
		OnConnected:    func(broker string) { log().Info("MQTT connected", "broker", broker) },
		OnDisconnected: func(broker string) { log().Info("MQTT disconnected", "broker", broker) },
	})
	if err := bridge.Start(); err != nil {
		log().Error("Failed to start MQTT bridge", "error", err)
		return nil
	}

	// Seed current device info, then keep the bridge updated as self-info and
	// stats arrive from the device.
	if info := client.GetSelfInfo(); info != nil {
		bridge.SetDeviceInfo(toDeviceInfo(info))
	}
	client.OnSelfInfo(func(info *meshcore.SelfInfo) { bridge.SetDeviceInfo(toDeviceInfo(info)) })
	client.OnStats(func(stats *meshcore.CompanionRadioStats) { bridge.SetDeviceStats(toDeviceStats(stats)) })

	client.SetPacketPublisher(bridge)
	return bridge
}

// toDeviceInfo converts meshcore SelfInfo to the MQTT bridge's DeviceInfo.
func toDeviceInfo(info *meshcore.SelfInfo) *mqttbridge.DeviceInfo {
	if info == nil {
		return nil
	}
	return &mqttbridge.DeviceInfo{
		PublicKey:       info.PublicKey,
		Name:            info.Name,
		TxPower:         info.TxPower,
		Latitude:        info.Latitude,
		Longitude:       info.Longitude,
		FreqHz:          info.FreqHz,
		BwHz:            info.BwHz,
		Sf:              info.Sf,
		Cr:              info.Cr,
		FirmwareVersion: info.FirmwareVersion,
		Model:           info.Model,
	}
}

// toDeviceStats converts meshcore CompanionRadioStats to the bridge's DeviceStats.
func toDeviceStats(stats *meshcore.CompanionRadioStats) *mqttbridge.DeviceStats {
	if stats == nil {
		return nil
	}
	return &mqttbridge.DeviceStats{
		BatteryMv:         stats.BatteryMv,
		UptimeSecs:        stats.UptimeSecs,
		Errors:            stats.Errors,
		QueueLen:          stats.QueueLen,
		TxAirSecs:         stats.TxAirSecs,
		RxAirSecs:         stats.RxAirSecs,
		NoiseFloorDbm:     stats.NoiseFloorDbm,
		LastRssi:          stats.LastRssi,
		LastSnrDb:         stats.LastSnrDb,
		PacketsRecv:       stats.PacketsRecv,
		PacketsSent:       stats.PacketsSent,
		PacketsFloodTx:    stats.PacketsFloodTx,
		PacketsDirectTx:   stats.PacketsDirectTx,
		PacketsFloodRx:    stats.PacketsFloodRx,
		PacketsDirectRx:   stats.PacketsDirectRx,
		PacketsRecvErrors: stats.PacketsRecvErrors,
	}
}

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

var inflight = &inflightSet{m: make(map[string]struct{})}

func processDM(m *meshcore.Message, client *meshcore.MeshCoreClient, outgoingCh chan<- outgoingMsg) {
	senderKeyHex := fmt.Sprintf("%x", m.SenderKey)
	if !config.Get().Security.Direct.IsAllowed(senderKeyHex) {
		log().Info("Direct message filtered by security policy", "sender", senderKeyHex[:12])
		return
	}

	// Look up the full contact record for the sender.
	shortKey := fmt.Sprintf("%x", m.SenderKey[:4])
	contact := client.FindContactByKeyPrefix(m.SenderKey[:6])
	if contact == nil {
		contact = &meshcore.Contact{
			PublicKey: m.SenderKey,
			Name:      fmt.Sprintf("?%x", m.SenderKey[:6]),
		}
	}

	log().Info("Direct message received", "sender", contact.Name, "key", shortKey, "text", m.Text)

	// One command in flight per sender: reply busy instead of queuing a second.
	if !inflight.begin("dm:" + senderKeyHex) {
		log().Info("Sender busy, replying busy", "sender", contact.Name)
		select {
		case outgoingCh <- outgoingMsg{contact: contact, text: "busy, try again", ack: true}:
		default:
			log().Warn("Outgoing channel full, dropping busy reply", "sender", contact.Name)
		}
		return
	}
	defer inflight.done("dm:" + senderKeyHex)

	if flood, ok := commands.ParseAdvertCommand(m.Text); ok {
		log().Info("Sending advert", "flood", flood)
		reply := "success"
		if err := client.SendSelfAdvert(flood); err != nil {
			log().Error("SendSelfAdvert failed", "error", err)
			reply = "error: " + err.Error()
		}
		outgoingCh <- outgoingMsg{contact: contact, text: reply, ack: true}
		return
	}

	pingMeta := buildPingMeta(m.PathLength, m.PathBytes, m.Timestamp, contact.Latitude, contact.Longitude, client.GetContacts())
	setSelfCoords(pingMeta, client)

	replies, name, err := commands.GenerateReplies(m.Text, "", pingMeta, string(contact.PublicKey), contact.Name)
	if err != nil {
		log().Error("Command error", "error", err)
		return
	}
	if len(replies) == 0 {
		log().Debug("No reply generated, skipping", "text", m.Text)
		return
	}

	for _, reply := range shrink.Fit(name, "", replies) {
		outgoingCh <- outgoingMsg{contact: contact, text: reply, ack: true}
	}
}

func processChannelMessage(m *meshcore.ChannelMessage, client *meshcore.MeshCoreClient, tgb *tgbridge.Bridge, outgoingCh chan<- outgoingMsg) {
	log().Info("Channel message received", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)

	channels := client.GetChannels()
	channel, ok := channels[m.ChannelIndex]
	if !ok || channel.IsEmpty {
		log().Warn("Channel not found or empty", "channel", m.ChannelIndex)
		return
	}

	if !config.Get().Security.Channels.IsAllowed(channel.Name) {
		log().Info("Channel message filtered by security policy", "channel", channel.Name)
		return
	}

	if tgb != nil {
		tgb.Forward(channel.Name, m.SenderName, m.Text)
	}

	// Resolve sender coordinates and public key for commands.
	var senderLat, senderLon *float64
	var senderKeyHexCh string
	if c := client.FindContactByName(m.SenderName); c != nil {
		senderLat = c.Latitude
		senderLon = c.Longitude
		senderKeyHexCh = fmt.Sprintf("%x", c.PublicKey)
	}

	// Nudge senders still routing with 1-byte hashes. m.PathBytes/m.PathLength
	// (meaningful path bytes + hop count) are populated for live messages; their
	// ratio is the bytes-per-hop. Done before the per-sender in-flight guard so it
	// fires for any message, not just commands, and never consumes a command slot.
	if hops := m.PathLength; hops > 0 && len(m.PathBytes) > 0 && len(m.PathBytes)%hops == 0 {
		warnKey := senderKeyHexCh
		if warnKey == "" {
			warnKey = "name:" + m.SenderName
		}
		warnOldHash(outgoingCh, channel, warnKey, m.SenderName, len(m.PathBytes)/hops)
	}

	// forwardBotReply sends a bot reply to Telegram only when this mesh channel
	// has a matching entry in the Targets config (Forward filters internally).
	botName := "Bot"
	if name := client.SelfName(); name != "" {
		botName = name
	}
	forwardBotReply := func(text string) {
		if tgb != nil && tgb.HasTarget(channel.Name) {
			tgb.Forward(channel.Name, botName, text)
		}
	}

	// One command in flight per sender: reply busy instead of queuing a second.
	if !inflight.begin("ch:" + m.SenderName) {
		log().Info("Sender busy, replying busy", "channel", channel.Name, "sender", m.SenderName)
		text := fmt.Sprintf("@[%s] busy, try again", m.SenderName)
		select {
		case outgoingCh <- outgoingMsg{channel: channel, text: text, ack: true}:
			forwardBotReply(text)
		default:
			log().Warn("Outgoing channel full, dropping busy reply", "channel", channel.Name)
		}
		return
	}
	defer inflight.done("ch:" + m.SenderName)

	if flood, ok := commands.ParseAdvertCommand(m.Text); ok {
		log().Info("Sending advert", "flood", flood)
		reply := "success"
		if err := client.SendSelfAdvert(flood); err != nil {
			log().Error("SendSelfAdvert failed", "error", err)
			reply = "error: " + err.Error()
		}
		text := fmt.Sprintf("@[%s] %s", m.SenderName, reply)
		outgoingCh <- outgoingMsg{channel: channel, text: text, ack: true}
		forwardBotReply(text)
		return
	}

	pingMeta := buildPingMeta(m.PathLength, m.PathBytes, m.Timestamp, senderLat, senderLon, client.GetContacts())
	setSelfCoords(pingMeta, client)

	replies, name, err := commands.GenerateReplies(m.Text, channel.Name, pingMeta, senderKeyHexCh, m.SenderName)
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
		outgoingCh <- outgoingMsg{channel: channel, text: text, ack: i == 0}
		forwardBotReply(text)
	}
}

// buildPingMeta constructs a PingMeta from raw message fields.
// It resolves intermediate hop coordinates from the contact list when available.
func buildPingMeta(pathLength int, pathBytes []byte, timestamp time.Time, senderLat, senderLon *float64, contacts map[string]*meshcore.Contact) *commands.PingMeta {
	meta := &commands.PingMeta{
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

// setSelfCoords fills pingMeta.SelfLat/SelfLon from config (when use_config_coords is true)
// or from the device's own SelfInfo. Config coords take priority when the flag is set.
func setSelfCoords(pingMeta *commands.PingMeta, client interface {
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

// splitMeshMessage splits text into parts that each fit within maxLen bytes when prefixed.
// The prefix is applied to every part. Splits on word boundaries where possible.
// resolveHopCoords resolves GPS coordinates for each intermediate hop in pathBytes.
// bytesPerHop is 1 or 2; each hop's bytes are matched against contact public key prefixes.
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
