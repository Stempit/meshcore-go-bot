package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"meshbot/internal/commands"
	"meshbot/internal/config"
	"meshbot/internal/historylog"
	meshcore "meshbot/internal/meshcore_bt"
	"meshbot/internal/proxy"
	"meshbot/internal/tgbridge"
)

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

func main() {
	// config
	if err := config.Load("config.yaml"); err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	proxyCfg := config.Get().Proxy
	if err := proxy.Init(proxyCfg.Enabled, proxyCfg.URL); err != nil {
		slog.Error("Failed to initialize proxy", "error", err)
		os.Exit(1)
	}
	if proxyCfg.Enabled {
		slog.Info("Proxy enabled", "url", proxyCfg.URL)
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
			slog.Error("Connection.serial_port must be set when Connection.type is \"serial\"")
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

	// mqtt init
	mqttCfg := config.Get().MQTT
	client.SetMQTTConfig(&mqttCfg)
	defer client.StopMQTT()
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
				if err := client.SendChannelMessageWithRetry(msg.channel, msg.text, msg.ack, 1, 15*time.Second); err != nil {
					slog.Error("Failed to send channel reply", "channel", msg.channel.Name, "error", err)
				} else {
					slog.Info("Channel reply sent", "channel", msg.channel.Name)
				}
			} else if msg.contact != nil {
				err := client.SendMessageWithRetry(msg.contact, msg.text, true, 5, 10*time.Second)
				if err != nil {
					err = client.SendMessageWithRetry(msg.contact, msg.text, false, 5, 10*time.Second)
				}
				if err != nil {
					slog.Error("Failed to send DM reply", "error", err)
				} else {
					slog.Info("DM reply sent")
				}
			}
			lastSent = time.Now()
		}
	}()

	// ─── Greet new nodes ──────────────────────────────────────────────────────

	knownKeys, err := commands.NewKnownKeys("log/known.txt")
	if err != nil {
		slog.Error("Failed to load known keys", "error", err)
		os.Exit(1)
	}

	if err := commands.InitTicketStore("log/tickets.txt"); err != nil {
		slog.Error("Failed to load ticket store", "error", err)
		os.Exit(1)
	}

	sendGreeting := func(contact *meshcore.Contact) {
		slog.Info("New node seen, sending greeting", "name", contact.Name, "type", contact.Type)
		pubCh := client.GetChannels()[0]
		if pubCh == nil || pubCh.IsEmpty {
			slog.Warn("Public channel 0 not available, skipping greeting")
			return
		}
		msg := commands.GreetMessage(contact.Name, contact.PublicKey, contact.Type)
		if err := client.SendChannelMessageWithRetry(pubCh, msg, true, 0, 15*time.Second); err != nil {
			slog.Error("Failed to send greeting", "channel", pubCh.Name, "error", err)
		} else {
			slog.Info("Channel greeting sent", "channel", pubCh.Name)
		}
	}

	// pendingGreets holds public-key hex strings of nodes whose advert arrived
	// before their contact record was in the cache. They will be greeted once
	// the full contact is received via OnContact.
	pendingGreets := make(map[string]struct{})
	var pendingMu sync.Mutex

	client.OnAdvert(func(contact *meshcore.Contact) {
		alreadyKnown, err := knownKeys.CheckAndAdd(contact.PublicKey, contact.Name)
		if err != nil {
			slog.Error("Failed to update known keys", "error", err)
			return
		}
		if alreadyKnown {
			return
		}
		if contact.Type == 0 && contact.Name == "" {
			// Stub contact: type/name unknown yet. Request a contacts refresh and
			// defer the greeting until the full record arrives via OnContact.
			keyHex := fmt.Sprintf("%x", contact.PublicKey)
			slog.Info("New node seen (stub), deferring greeting", "pub_key", keyHex)
			pendingMu.Lock()
			pendingGreets[keyHex] = struct{}{}
			pendingMu.Unlock()
			if err := client.RequestContacts(0); err != nil {
				slog.Error("Failed to request contacts after stub advert", "error", err)
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
			slog.Error("Failed to start Telegram bridge", "error", err)
		} else {
			tgb.OnMessage(func(meshChannelName, senderName, text string, shrink bool) {
				var tgChannel *meshcore.Channel
				for _, ch := range client.GetChannels() {
					if ch.Name == meshChannelName {
						tgChannel = ch
						break
					}
				}
				if tgChannel == nil {
					slog.Warn("Telegram bridge: meshcore channel not found", "name", meshChannelName)
					return
				}
				prefix := fmt.Sprintf("[TG:%s] ", senderName)
				var parts []string
				if shrink {
					shrunk, shrinkErr := commands.ShrinkText(text)
					if shrinkErr != nil {
						slog.Warn("Telegram bridge: shrink failed, splitting instead", "error", shrinkErr)
						parts = splitMeshMessage(prefix, text, 140)
					} else {
						parts = []string{prefix + shrunk}
					}
				} else {
					parts = splitMeshMessage(prefix, text, 140)
				}
				for _, part := range parts {
					outgoingCh <- outgoingMsg{channel: tgChannel, text: part, ack: false}
				}
			})
			go tgb.Start()
			defer tgb.Stop()
		}
	}

	// ─── Incoming processor goroutine ─────────────────────────────────────────

	go func() {
		for msg := range incomingCh {
			switch m := msg.(type) {
			case *meshcore.Message:
				processDM(m, client, outgoingCh)
			case *meshcore.ChannelMessage:
				processChannelMessage(m, client, tgb, outgoingCh)
			}
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
		slog.Error("Failed to sync stored messages", "error", err)
		close(syncDone)
	}

	select {
	case <-syncDone:
		slog.Info("Device sync complete")
	case <-time.After(15 * time.Second):
		slog.Warn("Device sync timed out, proceeding with messages received so far")
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
		slog.Info("Stored DM", "sender", fmt.Sprintf("%x", m.SenderKey[:6]), "text", m.Text)
		if processStoredMessages {
			incomingCh <- m
		}
	}
	for _, m := range client.GetAllChannelMessages() {
		slog.Info("Stored channel message", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)
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
		slog.Error("Failed to request initial stats", "error", err)
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
					slog.Error("Failed to request periodic stats", "error", err)
					if !client.IsConnected() {
						slog.Warn("BLE connection lost, reconnecting...")
						time.Sleep(5 * time.Second)
						if err := client.Reconnect(); err != nil {
							slog.Error("Reconnect failed", "error", err)
						} else {
							slog.Info("Reconnected to device")
						}
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	slog.Info("Bot running. Press Ctrl+C to exit.")
	<-ctx.Done()
	slog.Info("Shutting down...")
}

func processDM(m *meshcore.Message, client *meshcore.MeshCoreClient, outgoingCh chan<- outgoingMsg) {
	senderKeyHex := fmt.Sprintf("%x", m.SenderKey)
	if !config.Get().Security.Direct.IsAllowed(senderKeyHex) {
		slog.Info("Direct message filtered by security policy", "sender", senderKeyHex[:12])
		return
	}

	// Look up the full contact record for the sender.
	shortKey := fmt.Sprintf("%x", m.SenderKey[:6])
	var contact *meshcore.Contact
	for _, c := range client.GetContacts() {
		if fmt.Sprintf("%x", c.PublicKey[:6]) == shortKey {
			contact = c
			break
		}
	}
	if contact == nil {
		contact = &meshcore.Contact{
			PublicKey: m.SenderKey,
			Name:      fmt.Sprintf("?%x", m.SenderKey[:6]),
		}
	}

	slog.Info("Direct message received", "sender", shortKey, "text", m.Text)

	if flood, ok := commands.ParseAdvertCommand(m.Text); ok {
		slog.Info("Sending advert", "flood", flood)
		reply := "success"
		if err := client.SendSelfAdvert(flood); err != nil {
			slog.Error("SendSelfAdvert failed", "error", err)
			reply = "error: " + err.Error()
		}
		outgoingCh <- outgoingMsg{contact: contact, text: reply, ack: true}
		return
	}

	pingMeta := buildPingMeta(m.PathLength, m.PathBytes, m.Timestamp, contact.Latitude, contact.Longitude, client.GetContacts())
	setSelfCoords(pingMeta, client)

	replies, err := commands.GenerateReplies(m.Text, "", pingMeta, string(contact.PublicKey), contact.Name)
	if err != nil {
		slog.Error("Command error", "error", err)
		return
	}
	if len(replies) == 0 {
		slog.Debug("No reply generated, skipping", "text", m.Text)
		return
	}

	for _, reply := range replies {
		outgoingCh <- outgoingMsg{contact: contact, text: reply, ack: true}
	}
}

func processChannelMessage(m *meshcore.ChannelMessage, client *meshcore.MeshCoreClient, tgb *tgbridge.Bridge, outgoingCh chan<- outgoingMsg) {
	slog.Info("Channel message received", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)

	channels := client.GetChannels()
	channel, ok := channels[m.ChannelIndex]
	if !ok || channel.IsEmpty {
		slog.Warn("Channel not found or empty", "channel", m.ChannelIndex)
		return
	}

	if !config.Get().Security.Channels.IsAllowed(channel.Name) {
		slog.Info("Channel message filtered by security policy", "channel", channel.Name)
		return
	}

	if tgb != nil {
		tgb.Forward(channel.Name, m.SenderName, m.Text)
	}

	// Resolve sender coordinates and public key for commands.
	var senderLat, senderLon *float64
	var senderKeyHexCh string
	for _, c := range client.GetContacts() {
		if c.Name == m.SenderName {
			senderLat = c.Latitude
			senderLon = c.Longitude
			senderKeyHexCh = fmt.Sprintf("%x", c.PublicKey)
			break
		}
	}

	if flood, ok := commands.ParseAdvertCommand(m.Text); ok {
		slog.Info("Sending advert", "flood", flood)
		reply := "success"
		if err := client.SendSelfAdvert(flood); err != nil {
			slog.Error("SendSelfAdvert failed", "error", err)
			reply = "error: " + err.Error()
		}
		outgoingCh <- outgoingMsg{channel: channel, text: fmt.Sprintf("@[%s] %s", m.SenderName, reply), ack: true}
		return
	}

	pingMeta := buildPingMeta(m.PathLength, m.PathBytes, m.Timestamp, senderLat, senderLon, client.GetContacts())
	setSelfCoords(pingMeta, client)

	replies, err := commands.GenerateReplies(m.Text, channel.Name, pingMeta, senderKeyHexCh, m.SenderName)
	if err != nil {
		slog.Error("Command error", "error", err)
		return
	}
	if len(replies) == 0 {
		slog.Debug("No reply generated, skipping", "text", m.Text)
		return
	}

	for i, reply := range replies {
		text := reply
		if len(replies) == 1 {
			text = fmt.Sprintf("@[%s] %s", m.SenderName, reply)
		}
		outgoingCh <- outgoingMsg{channel: channel, text: text, ack: i == 0}
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
func splitMeshMessage(prefix, text string, maxLen int) []string {
	budget := maxLen - len(prefix)
	if budget <= 0 {
		return []string{prefix}
	}
	if len(prefix)+len(text) <= maxLen {
		return []string{prefix + text}
	}

	var parts []string
	for len(text) > 0 {
		if len(text) <= budget {
			parts = append(parts, prefix+text)
			break
		}
		// find last space within budget
		cut := budget
		if i := strings.LastIndex(text[:cut], " "); i > 0 {
			cut = i
		}
		parts = append(parts, prefix+text[:cut])
		text = strings.TrimLeft(text[cut:], " ")
	}
	return parts
}

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
