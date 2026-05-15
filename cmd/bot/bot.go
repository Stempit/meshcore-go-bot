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
	"meshbot/internal/tgbridge"
)

func main() {
	// config
	if err := config.Load("config.yaml"); err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// history logger
	histlog := historylog.New("log/history")
	defer histlog.Close()

	// meshcore client
	client := meshcore.CreateClient()
	defer client.Disconnect()

	// log history in client
	client.SetHistoryLogger(histlog)

	// mqtt init
	mqttCfg := config.Get().MQTT
	client.SetMQTTConfig(&mqttCfg)
	client.RequestAllStats()

	// ─── Greet new nodes ──────────────────────────────────────────────────────

	knownKeys, err := commands.NewKnownKeys("log/known.txt")
	if err != nil {
		slog.Error("Failed to load known keys", "error", err)
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
		if err := client.SendChannelMessageWithRetry(pubCh, msg, false, 3, 5*time.Second); err != nil {
			slog.Error("Failed to send greeting", "error", err)
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
		tgb, err = tgbridge.New(tgCfg, slog.Default())
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
				for i, part := range parts {
					if i > 0 {
						time.Sleep(3 * time.Second)
					}
					if err := client.SendChannelMessageWithRetry(tgChannel, part, false, 3, 5*time.Second); err != nil {
						slog.Error("Telegram bridge: failed to send to meshcore", "error", err)
					}
				}
			})
			go tgb.Start()
			defer tgb.Stop()
		}
	}

	// ─── Sync stored messages ─────────────────────────────────────────────────

	syncDone := make(chan struct{})
	client.OnNoMoreMessages(func() {
		for _, m := range client.GetAllMessages() {
			slog.Info("Stored DM", "sender", fmt.Sprintf("%x", m.SenderKey[:6]), "text", m.Text)
		}
		for _, m := range client.GetAllChannelMessages() {
			slog.Info("Stored channel message", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)
		}
		close(syncDone)
	})
	if err := client.SyncNextMessage(); err != nil {
		slog.Error("Failed to sync stored messages", "error", err)
		close(syncDone)
	}
	<-syncDone

	// Drain messages buffered during sync so we only respond to new arrivals.
drainLoop:
	for {
		select {
		case <-client.IncomingMessages():
		default:
			break drainLoop
		}
	}

	// ─── Process incoming messages ────────────────────────────────────────────

	go func() {
		for msg := range client.IncomingMessages() {
			switch m := msg.(type) {
			case *meshcore.Message:
				processDM(m, client)
			case *meshcore.ChannelMessage:
				processChannelMessage(m, client, tgb)
			}
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

func processDM(m *meshcore.Message, client *meshcore.MeshCoreClient) {
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

	pingMeta := buildPingMeta(m.PathLength, m.PathBytes, m.Timestamp, contact.Latitude, contact.Longitude, client.GetContacts())
	setSelfCoords(pingMeta, client)

	replies, err := commands.GenerateReplies(m.Text, "", pingMeta)
	if err != nil {
		slog.Error("Command error", "error", err)
		return
	}
	if len(replies) == 0 {
		slog.Debug("No reply generated, skipping", "text", m.Text)
		return
	}

	for i, reply := range replies {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		err = client.SendMessageWithRetry(contact, reply, true, 5, 5*time.Second)
		if err != nil {
			// Fall back to send-without-delivery-confirmation.
			err = client.SendMessageWithRetry(contact, reply, false, 5, 5*time.Second)
		}
		if err != nil {
			slog.Error("Failed to send DM reply", "error", err)
		} else {
			slog.Info("DM reply sent")
		}
	}
}

func processChannelMessage(m *meshcore.ChannelMessage, client *meshcore.MeshCoreClient, tgb *tgbridge.Bridge) {
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

	// Resolve sender coordinates for ping/trace commands.
	var senderLat, senderLon *float64
	for _, c := range client.GetContacts() {
		if c.Name == m.SenderName {
			senderLat = c.Latitude
			senderLon = c.Longitude
			break
		}
	}

	pingMeta := buildPingMeta(m.PathLength, m.PathBytes, m.Timestamp, senderLat, senderLon, client.GetContacts())
	setSelfCoords(pingMeta, client)

	replies, err := commands.GenerateReplies(m.Text, channel.Name, pingMeta)
	if err != nil {
		slog.Error("Command error", "error", err)
		return
	}
	if len(replies) == 0 {
		slog.Debug("No reply generated, skipping", "text", m.Text)
		return
	}

	for i, reply := range replies {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		text := reply
		if len(replies) == 1 {
			text = fmt.Sprintf("@[%s] %s", m.SenderName, reply)
		}
		if err := client.SendChannelMessageWithRetry(channel, text, false, 5, 5*time.Second); err != nil {
			slog.Error("Failed to send channel reply", "channel", channel.Name, "error", err)
		} else {
			slog.Info("Channel reply sent", "channel", channel.Name)
		}
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
