// Command web runs the meshbot app as an HTTP gateway server: received mesh
// messages accumulate in an unread inbox exposed via GET /get, and external
// clients send mesh messages via POST /send. See internal/web for the API.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"meshbot/internal/companion"
	"meshbot/internal/config"
	"meshbot/internal/logging"
	"meshbot/internal/web"
)

var log = logging.For("web")

func main() {
	if err := config.Load("config.yaml"); err != nil {
		log().Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	// ─── HTTP server ──────────────────────────────────────────────────────────

	srvCfg := config.Get().Server
	listen := srvCfg.Listen
	if listen == "" {
		listen = ":8080"
	}

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           web.NewServer(client, svc.Inbox, srvCfg.Token, slog.Default()).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// Blocking sends wait through device confirmations and retry pacing —
		// tens of seconds worst case — so writes get a generous ceiling.
		WriteTimeout: 90 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.ListenAndServe() }()

	log().Info("Web server listening", "addr", listen, "auth", srvCfg.Token != "")
	log().Info("Endpoints: GET /get, GET /messages, GET /contacts, GET /channels, POST /send")

	select {
	case <-ctx.Done():
		log().Info("Shutting down...")
	case err := <-serveErr:
		log().Error("HTTP server failed", "error", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log().Error("HTTP server shutdown error", "error", err)
	}
}
