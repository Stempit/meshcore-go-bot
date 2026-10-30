package agentclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsServer upgrades the single connection and runs handler on it.
func wsServer(t *testing.T, handler func(r *http.Request, conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handler(r, conn)
	}))
}

func TestPicoClawAsk(t *testing.T) {
	frames := make(chan picoMessage, 1)
	srv := wsServer(t, func(r *http.Request, conn *websocket.Conn) {
		if got := r.URL.Query().Get("session_id"); got != "mesh-ch-gen" {
			t.Errorf("query session_id = %q, want mesh-ch-gen", got)
		}
		var sent picoMessage
		if err := conn.ReadJSON(&sent); err != nil {
			t.Errorf("read message.send: %v", err)
			return
		}
		frames <- sent
		_ = conn.WriteJSON(picoMessage{Type: "typing.start"})
		_ = conn.WriteJSON(picoMessage{
			Type:    "message.create",
			ID:      "srv-1",
			Payload: map[string]any{"message_id": "m1", "content": "hello from picoclaw"},
		})
		_ = conn.WriteJSON(picoMessage{Type: "typing.stop"})
		// Hold the connection so the 500 ms drain window fires client-side,
		// then close: Ask must return what it already has.
		time.Sleep(600 * time.Millisecond)
	})
	defer srv.Close()

	c, err := New(Config{Type: "picoclaw", URL: "ws://" + strings.TrimPrefix(srv.URL, "http://"), Token: "tok"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parts, err := c.Ask(context.Background(), "mesh-ch-gen", "hi")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(parts) != 1 || parts[0] != "hello from picoclaw" {
		t.Fatalf("parts = %q, want [hello from picoclaw]", parts)
	}

	select {
	case sent := <-frames:
		if sent.Type != "message.send" || sent.SessionID != "mesh-ch-gen" {
			t.Errorf("sent frame = %+v, want message.send with session mesh-ch-gen", sent)
		}
		if content, _ := sent.Payload["content"].(string); content != "hi" {
			t.Errorf("sent payload content = %v, want hi", sent.Payload["content"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received message.send")
	}
}

func TestPicoClawAskMultiMessage(t *testing.T) {
	srv := wsServer(t, func(_ *http.Request, conn *websocket.Conn) {
		var m picoMessage
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		_ = conn.WriteJSON(picoMessage{Type: "message.create", Payload: map[string]any{"message_id": "a", "content": "one"}})
		// update rewrites part a and adds a second part keyed by top-level ID
		_ = conn.WriteJSON(picoMessage{Type: "message.update", ID: "a", Payload: map[string]any{"content": "one!"}})
		_ = conn.WriteJSON(picoMessage{Type: "message.update", ID: "b", Payload: map[string]any{"content": "two"}})
		_ = conn.WriteJSON(picoMessage{Type: "typing.stop"})
		time.Sleep(600 * time.Millisecond)
	})
	defer srv.Close()

	c, _ := New(Config{Type: "picoclaw", URL: "ws://" + strings.TrimPrefix(srv.URL, "http://")})
	parts, err := c.Ask(context.Background(), "s", "hi")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	want := []string{"one!", "two"}
	if len(parts) != len(want) {
		t.Fatalf("parts = %q, want %q", parts, want)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("parts = %q, want %q", parts, want)
		}
	}
}

func TestPicoClawAskErrorFrame(t *testing.T) {
	srv := wsServer(t, func(_ *http.Request, conn *websocket.Conn) {
		var m picoMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(picoMessage{Type: "error", Payload: map[string]any{"code": "boom", "message": "bad request"}})
		time.Sleep(100 * time.Millisecond)
	})
	defer srv.Close()

	c, _ := New(Config{Type: "picoclaw", URL: "ws://" + strings.TrimPrefix(srv.URL, "http://")})
	_, err := c.Ask(context.Background(), "s", "hi")
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "bad request") {
		t.Errorf("error = %v, want error boom: bad request", err)
	}
}

func TestPicoClawAskNoResponse(t *testing.T) {
	srv := wsServer(t, func(_ *http.Request, conn *websocket.Conn) {
		var m picoMessage
		_ = conn.ReadJSON(&m)
		// Only invisible traffic, then close: Ask must fail with
		// "no response received" instead of returning an empty reply.
		_ = conn.WriteJSON(picoMessage{Type: "message.create", Payload: map[string]any{"kind": "thought", "content": "invisible"}})
		_ = conn.WriteJSON(picoMessage{Type: "typing.stop"})
		time.Sleep(100 * time.Millisecond)
	})
	defer srv.Close()

	c, _ := New(Config{Type: "picoclaw", URL: "ws://" + strings.TrimPrefix(srv.URL, "http://")})
	_, err := c.Ask(context.Background(), "s", "hi")
	if err == nil || !strings.Contains(err.Error(), "no response received") {
		t.Errorf("error = %v, want no response received", err)
	}
}

func TestPicoClawAskInvisibleExtendsWindow(t *testing.T) {
	srv := wsServer(t, func(_ *http.Request, conn *websocket.Conn) {
		var m picoMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(picoMessage{Type: "message.create", Payload: map[string]any{"kind": "tool_calls", "content": "working"}})
		_ = conn.WriteJSON(picoMessage{Type: "typing.stop"})
		// Arrives after the 500 ms drain window would have fired, but the
		// invisible message must have extended the deadline to 300 s.
		time.Sleep(1200 * time.Millisecond)
		_ = conn.WriteJSON(picoMessage{Type: "message.create", Payload: map[string]any{"message_id": "late", "content": "done"}})
		time.Sleep(600 * time.Millisecond)
	})
	defer srv.Close()

	c, _ := New(Config{Type: "picoclaw", URL: "ws://" + strings.TrimPrefix(srv.URL, "http://")})
	parts, err := c.Ask(context.Background(), "s", "hi")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(parts) != 1 || parts[0] != "done" {
		t.Fatalf("parts = %q, want [done]", parts)
	}
}

func TestPicoClawNeedsURL(t *testing.T) {
	if _, err := New(Config{Type: "picoclaw"}); err == nil {
		t.Fatal("New without url must fail")
	}
}

func TestNewUnknownType(t *testing.T) {
	_, err := New(Config{Type: "carrier-pigeon"})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("error = %v, want unknown type named", err)
	}
}
