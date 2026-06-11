package picoclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

type picoMessage struct {
	Type      string         `json:"type"`
	ID        string         `json:"id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Timestamp int64          `json:"timestamp,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// Ask sends text to a picoclaw gateway over the Pico Protocol WebSocket and returns
// the agent's response parts. Each element is one visible message.create/update from
// the agent — typically one item, but multi-message turns are preserved.
//
// sessionID should be stable per-user so conversation history is maintained.
// wsURL must point directly at the picoclaw gateway (default port 18790), e.g.
// "ws://host:18790/pico/ws". The web dashboard port (18800) uses session-cookie
// auth and cannot be used here.
func Ask(ctx context.Context, wsURL, token, sessionID, text string) ([]string, error) {
	// Pass session_id as a query parameter so the gateway registers the connection
	// under this session and can route responses back to us.
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	q := u.Query()
	q.Set("session_id", sessionID)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{"Authorization": {"Bearer " + token}}

	conn, resp, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("connect (HTTP %d): %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	// Close the connection when the context expires so ReadMessage unblocks.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	send := picoMessage{
		Type:      "message.send",
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		SessionID: sessionID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   map[string]any{"content": text},
	}
	if err := conn.WriteJSON(send); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	// Track visible content per message ID.
	// message.create uses payload["message_id"]; message.update uses the top-level ID.
	msgContent := map[string]string{}
	var msgOrder []string
	var typingStopped bool

	// afterVisible resets the deadline to a short window for collecting trailing messages.
	afterVisible := func() {
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	}
	// afterInvisible extends the deadline when a tool call / thought message arrives —
	// the agent is still working and the final response will come later.
	afterInvisible := func() {
		conn.SetReadDeadline(time.Now().Add(300 * time.Second))
	}

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if !typingStopped && ctx.Err() != nil {
				return nil, fmt.Errorf("timeout waiting for response (received %d messages)", len(msgOrder))
			}
			// Drain deadline fired or connection closed — exit and return accumulated.
			break
		}
		var msg picoMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		slog.Debug("picoclaw message", "type", msg.Type, "id", msg.ID)

		switch msg.Type {
		case "typing.start":
			slog.Info("picoclaw typing started", "session", sessionID)

		case "message.create":
			if isVisible(msg.Payload) {
				// Server puts the message ID in payload["message_id"], not the top-level ID field.
				msgID, _ := msg.Payload["message_id"].(string)
				if msgID == "" {
					msgID = msg.ID
				}
				content, _ := msg.Payload["content"].(string)
				msgContent[msgID] = content
				msgOrder = append(msgOrder, msgID)
				slog.Debug("picoclaw message.create", "msg_id", msgID, "content_len", len(content))
				if typingStopped && content != "" {
					afterVisible()
				}
			} else {
				slog.Debug("picoclaw message.create (invisible)", "kind", msg.Payload["kind"])
				if typingStopped {
					// Invisible message (tool call / thought) means the agent is still
					// working. Extend the deadline to give the tool time to complete.
					afterInvisible()
				}
			}

		case "message.update":
			if isVisible(msg.Payload) {
				if content, _ := msg.Payload["content"].(string); content != "" {
					if _, seen := msgContent[msg.ID]; !seen {
						msgOrder = append(msgOrder, msg.ID)
					}
					msgContent[msg.ID] = content
					slog.Debug("picoclaw message.update", "msg_id", msg.ID, "content_len", len(content))
					if typingStopped {
						afterVisible()
					}
				}
			}

		case "typing.stop":
			typingStopped = true
			msgs := buildMessages(msgOrder, msgContent)
			slog.Info("picoclaw typing stopped", "session", sessionID, "messages", len(msgs))
			if len(msgs) > 0 {
				// Messages already arrived before typing.stop (normal ordering).
				// Use a short drain window for any trailing messages.
				afterVisible()
			} else {
				// picoclaw sends typing.stop BEFORE message.create (via preSend).
				// Use a longer window for the incoming message.
				conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			}

		case "error":
			code, _ := msg.Payload["code"].(string)
			message, _ := msg.Payload["message"].(string)
			return nil, fmt.Errorf("error %s: %s", code, message)
		}
	}

	msgs := buildMessages(msgOrder, msgContent)
	if len(msgs) == 0 {
		return nil, fmt.Errorf("no response received")
	}
	return msgs, nil
}

// buildMessages returns the non-empty visible message contents in order.
func buildMessages(order []string, content map[string]string) []string {
	var result []string
	for _, id := range order {
		if c := content[id]; c != "" {
			result = append(result, c)
		}
	}
	return result
}

func isVisible(payload map[string]any) bool {
	kind, _ := payload["kind"].(string)
	switch kind {
	case "thought", "tool_calls":
		return false
	}
	return true
}
