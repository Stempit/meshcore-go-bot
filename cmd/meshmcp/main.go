// Command meshmcp bridges the XiaoZhi voice assistant (xiaozhi.me) to a
// MeshCore LoRa device. It implements the xiaozhi.me "MCP access point"
// protocol: the bridge dials OUT to the console-issued endpoint over
// WebSocket and serves MCP tools (read mesh messages, send, list contacts
// and channels), so it works behind ISP NAT with no port forwarding.
//
// It is a standalone feature with its own device connection (the Connection
// config section) — independent of the cmd/web HTTP gateway. Don't run it
// against the same physical device as cmd/web or cmd/bot at the same time:
// a companion device serves one connection.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"meshbot/internal/companion"
	"meshbot/internal/config"
	"meshbot/internal/logging"
	"meshbot/internal/shrink"
	"meshbot/internal/xiaozhi"
)

var log = logging.For("meshmcp")

func main() {
	if err := config.Load("config.yaml"); err != nil {
		log().Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	shrink.ValidateConfig()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.Get().MCP
	if cfg.EndpointURL == "" {
		log().Error(`MCP.endpoint_url must be set in config.yaml — copy the agent's "MCP access point" URL from the xiaozhi.me console`)
		os.Exit(1)
	}

	// Meshcore client — BLE (default) or USB serial depending on config.
	conn := config.Get().Connection
	client, err := companion.Connect(companion.ConnectOptions{
		Type:       conn.Type,
		SerialPort: conn.SerialPort,
		SerialBaud: conn.SerialBaud,
	})
	if err != nil {
		log().Error("Failed to connect to device", "error", err)
		os.Exit(1)
	}
	defer client.Disconnect()

	// Device runtime: startup stored-message sync, live forwarding into the
	// inbox, keepalive/reconnect loop.
	svc := companion.NewService(client, slog.Default())
	svc.Start(ctx)

	// Voice tools backed directly by the device + inbox; the voice cursor is
	// primed AFTER the startup sync, so "new messages" means arrivals from now
	// on — the backlog that existed before startup is not read aloud.
	mesh := xiaozhi.NewDirectMesh(client, svc.Inbox, xiaozhi.DirectMeshOptions{
		ChannelTransmissions: config.Get().Connection.ChannelTransmissions,
	})
	srv := xiaozhi.NewServer(mesh, svc.Inbox.LastID(), shrink.OptionsFor("xiaozhi"), slog.Default())

	sess, err := xiaozhi.NewSession(cfg.EndpointURL, srv, slog.Default())
	if err != nil {
		log().Error("Invalid MCP configuration", "error", err)
		os.Exit(1)
	}

	log().Info("meshmcp running", "endpoint", sess.EndpointSafe(), "cursor", svc.Inbox.LastID())
	sess.Run(ctx)
	log().Info("Shutting down")
}
