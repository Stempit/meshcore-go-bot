// Package companion is the shared runtime for the MeshCore companion device
// used by the standalone commands (the HTTP gateway cmd/web, the XiaoZhi MCP
// bridge cmd/meshmcp): device connection, the startup stored-message sync, a
// live forwarder into an Inbox, and the keepalive/reconnect loop. Each
// command runs its own Service — one companion connection per physical
// device. The wire protocol itself lives in internal/meshcore.
package companion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"meshbot/internal/config"
	"meshbot/internal/meshcore"
)

// Connect opens the meshcore companion device per cfg — BLE scan by default,
// or USB serial when cfg.Type is "serial" — and returns a ready client. The
// underlying constructors log and panic when the transport itself cannot be
// established; only configuration errors come back as an error.
func Connect(cfg config.ConnectionConfig) (*meshcore.MeshCoreClient, error) {
	switch cfg.Type {
	case "serial":
		if cfg.SerialPort == "" {
			return nil, errors.New(`Connection.serial_port must be set when Connection.type is "serial"`)
		}
		baud := cfg.SerialBaud
		if baud == 0 {
			baud = 115200
		}
		return meshcore.CreateSerialClient(cfg.SerialPort, baud), nil
	default:
		return meshcore.CreateClient(), nil
	}
}

// Service pairs a connected client with an Inbox and owns the device-side
// runtime: startup sync, live forwarding into the inbox, keepalive.
type Service struct {
	Client *meshcore.MeshCoreClient
	Inbox  *Inbox

	log *slog.Logger
}

// NewService binds a connected client to a fresh Inbox.
func NewService(client *meshcore.MeshCoreClient, logger *slog.Logger) *Service {
	return &Service{Client: client, Inbox: &Inbox{}, log: logger}
}

const (
	syncWait       = 15 * time.Second // bound on the device stored-message sync
	postSyncSettle = 2 * time.Second  // BLE settle time after sync before sending
)

// Start runs the device-side runtime. It blocks while stored messages are
// synced from the device into the inbox (bounded by syncWait), then returns;
// the live forwarder and keepalive loops run until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	s.syncStored(ctx)
	s.startForwarder()
	go s.keepalive(ctx)
}

// syncStored pulls messages stored on the device while the process was down
// into the inbox, so they arrive as unread on the first read.
func (s *Service) syncStored(ctx context.Context) {
	syncDone := make(chan struct{})
	s.Client.OnNoMoreMessages(func() { close(syncDone) })
	if err := s.Client.SyncNextMessage(); err != nil {
		s.log.Error("Failed to sync stored messages", "error", err)
		close(syncDone)
	}

	select {
	case <-syncDone:
		s.log.Info("Device sync complete")
	case <-time.After(syncWait):
		s.log.Warn("Device sync timed out, proceeding with messages received so far")
	case <-ctx.Done():
		return
	}
	// Neutralize the callback: a late "no more messages" during live operation
	// would panic on the already-closed syncDone channel.
	s.Client.OnNoMoreMessages(func() {})

	// Give the BLE device time to settle after the sync loop before any send.
	time.Sleep(postSyncSettle)

drainLoop:
	for {
		select {
		case <-s.Client.IncomingMessages():
		default:
			break drainLoop
		}
	}

	for _, m := range s.Client.GetAllMessages() {
		s.Inbox.Add(dmMessage(s.Client, m))
	}
	for _, m := range s.Client.GetAllChannelMessages() {
		s.Inbox.Add(channelMessage(s.Client, m))
	}
	s.log.Info("Stored messages queued as unread", "count", s.Inbox.Len())
}

// startForwarder pumps live arrivals into the inbox.
func (s *Service) startForwarder() {
	go func() {
		for msg := range s.Client.IncomingMessages() {
			switch m := msg.(type) {
			case *meshcore.Message:
				s.Inbox.Add(dmMessage(s.Client, m))
			case *meshcore.ChannelMessage:
				s.Inbox.Add(channelMessage(s.Client, m))
			}
		}
	}()
}

// keepalive polls device stats; a failed request on a disconnected BLE link
// triggers a reconnect.
func (s *Service) keepalive(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.Client.RequestAllStats(); err != nil {
				s.log.Error("Failed to request periodic stats", "error", err)
				if !s.Client.IsConnected() {
					s.log.Warn("BLE connection lost, reconnecting...")
					time.Sleep(5 * time.Second)
					if err := s.Client.Reconnect(); err != nil {
						s.log.Error("Reconnect failed", "error", err)
					} else {
						s.log.Info("Reconnected to device")
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// dmMessage converts a mesh DM into the consumer shape, resolving the
// sender's contact name from the 6-byte key prefix carried by the message.
func dmMessage(client *meshcore.MeshCoreClient, m *meshcore.Message) Message {
	prefix := min(6, len(m.SenderKey))
	sender := fmt.Sprintf("?%x", m.SenderKey[:prefix])
	if c := client.FindContactByKeyPrefix(m.SenderKey[:prefix]); c != nil {
		sender = c.Name
	}
	return Message{
		Type:         "dm",
		Sender:       sender,
		SenderKey:    fmt.Sprintf("%x", m.SenderKey),
		ChannelIndex: -1,
		Text:         m.Text,
		Timestamp:    m.Timestamp,
	}
}

// channelMessage converts a mesh channel message into the consumer shape.
func channelMessage(client *meshcore.MeshCoreClient, m *meshcore.ChannelMessage) Message {
	channel := ""
	if ch, ok := client.GetChannels()[m.ChannelIndex]; ok && !ch.IsEmpty {
		channel = ch.Name
	}
	return Message{
		Type:         "channel",
		Sender:       m.SenderName,
		Channel:      channel,
		ChannelIndex: m.ChannelIndex,
		Text:         m.Text,
		Timestamp:    m.Timestamp,
	}
}
