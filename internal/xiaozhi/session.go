package xiaozhi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// reconnect pacing: doubled after each failed/short-lived connection, reset
// after a connection that stayed up long enough to look healthy.
const (
	backoffStart = 2 * time.Second
	backoffMax   = 60 * time.Second
	stableAfter  = 60 * time.Second
)

// Session manages the outbound WebSocket connection to the xiaozhi.me MCP
// access point: dial, serve JSON-RPC frames until the connection drops, then
// reconnect with exponential backoff until the context is cancelled.
type Session struct {
	endpoint string
	server   *Server
	log      *slog.Logger
}

// NewSession builds a Session for the access-point URL from the xiaozhi.me
// console. The console prints an https:// URL; the https/http schemes are
// normalized to wss/ws for the WebSocket dial.
func NewSession(endpoint string, server *Server, logger *slog.Logger) (*Session, error) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		endpoint = "wss://" + strings.TrimPrefix(endpoint, "https://")
	case strings.HasPrefix(endpoint, "http://"):
		endpoint = "ws://" + strings.TrimPrefix(endpoint, "http://")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, fmt.Errorf("invalid MCP endpoint URL %q", endpoint)
	}
	return &Session{endpoint: endpoint, server: server, log: logger}, nil
}

// EndpointSafe returns the endpoint URL with any token query parameter
// redacted, for logging.
func (s *Session) EndpointSafe() string {
	u, err := url.Parse(s.endpoint)
	if err != nil {
		return "(unparseable endpoint)"
	}
	u.RawQuery = ""
	return u.String()
}

// Run connects and serves until ctx is cancelled, reconnecting with backoff.
func (s *Session) Run(ctx context.Context) {
	backoff := backoffStart
	for {
		started := time.Now()
		err := s.connectAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		s.log.Warn("MCP access point connection lost, reconnecting",
			"error", err, "endpoint", s.EndpointSafe(), "retry_in", backoff.Round(time.Second))

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if time.Since(started) >= stableAfter {
			backoff = backoffStart
		} else {
			backoff *= 2
			if backoff > backoffMax {
				backoff = backoffMax
			}
		}
	}
}

// connectAndServe dials the access point and pumps frames until an error or
// ctx cancellation. The dial-out direction is what makes this work behind
// NAT: xiaozhi.me never connects to us.
func (s *Session) connectAndServe(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, s.endpoint, nil)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return fmt.Errorf("dial %s: %w (status %d, body %s)", s.EndpointSafe(), err, resp.StatusCode, body)
		}
		return fmt.Errorf("dial %s: %w", s.EndpointSafe(), err)
	}
	defer conn.Close()

	// Closing the conn on shutdown unblocks ReadMessage below.
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stopped:
		}
	}()

	s.log.Info("Connected to MCP access point", "endpoint", s.EndpointSafe())

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if out := s.server.HandleMessage(data); out != nil {
			if err := conn.WriteMessage(websocket.TextMessage, out); err != nil {
				return err
			}
		}
	}
}
