package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"meshbot/internal/commands"
	"meshbot/internal/config"
	"meshbot/internal/dispatch"
	"meshbot/internal/greeter"
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

	// ─── Message engine ───────────────────────────────────────────────────────
	// The dispatcher owns the inbound worker pool and the serialized outbound
	// sender. Subsystems (greeter, Telegram) are wired in before it starts.
	d := dispatch.New(client)

	// ─── Greet new nodes + hash migration nudge ───────────────────────────────

	knownKeys, err := commands.NewKnownKeys("log/known.txt")
	if err != nil {
		log().Error("Failed to load known keys", "error", err)
		os.Exit(1)
	}

	if err := commands.InitTicketStore("log/tickets.txt"); err != nil {
		log().Error("Failed to load ticket store", "error", err)
		os.Exit(1)
	}

	migCfg := config.Get().Migration
	migMsg := migCfg.Message
	if migMsg == "" {
		migMsg = "switch to 2-byte please"
	}
	migCooldown := time.Duration(migCfg.CooldownSec) * time.Second
	if migCooldown <= 0 {
		migCooldown = 24 * time.Hour
	}
	notifier := commands.NewMigrationNotifier(migCfg.Enabled, migMsg, migCooldown)

	g := greeter.New(client, knownKeys, notifier, d.GreeterSender())
	g.Register()
	d.SetGreeter(g)

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
					d.Enqueue(dispatch.Outgoing{Channel: tgChannel, Text: part, Ack: false})
				}
			})
			go tgb.Start()
			defer tgb.Stop()
		}
	}
	d.SetTelegram(tgb)

	// Start the engine now that all subsystems are wired in.
	d.Run(ctx)

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
			d.Submit(m)
		}
	}
	for _, m := range client.GetAllChannelMessages() {
		log().Info("Stored channel message", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)
		if processStoredMessages {
			d.Submit(m)
		}
	}

	// ─── Live message forwarder goroutine ─────────────────────────────────────
	// All messages from here on are new live arrivals.

	go func() {
		for msg := range client.IncomingMessages() {
			d.Submit(msg)
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
		bridge.SetDeviceInfo(mqttbridge.DeviceInfoFromMesh(info))
	}
	client.OnSelfInfo(func(info *meshcore.SelfInfo) { bridge.SetDeviceInfo(mqttbridge.DeviceInfoFromMesh(info)) })
	client.OnStats(func(stats *meshcore.CompanionRadioStats) { bridge.SetDeviceStats(mqttbridge.DeviceStatsFromMesh(stats)) })

	client.SetPacketPublisher(bridge)
	return bridge
}
