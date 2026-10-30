package agentclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// serveChat spins up an OpenAI-compatible test server that captures the
// request and replies with respBody and status.
func serveChat(t *testing.T, status int, respBody string, captured *chatRequest, headers *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if headers != nil {
			*headers = r.Header.Clone()
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if captured != nil {
			if err := json.Unmarshal(body, captured); err != nil {
				t.Errorf("decode request body %q: %v", body, err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

func TestOpenAIAsk(t *testing.T) {
	var captured chatRequest
	var headers http.Header
	srv := serveChat(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"hello there"}}]}`,
		&captured, &headers)
	defer srv.Close()

	c, err := New(Config{Type: "openai", URL: srv.URL, Token: "sekrit"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parts, err := c.Ask(context.Background(), "mesh-dm-0102", "hi")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(parts) != 1 || parts[0] != "hello there" {
		t.Fatalf("parts = %q, want [hello there]", parts)
	}

	if got := headers.Get("Authorization"); got != "Bearer sekrit" {
		t.Errorf("Authorization = %q, want Bearer sekrit", got)
	}
	if captured.SessionID != "mesh-dm-0102" {
		t.Errorf("session_id = %q, want mesh-dm-0102", captured.SessionID)
	}
	if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" || captured.Messages[0].Content != "hi" {
		t.Errorf("messages = %+v, want one user message \"hi\"", captured.Messages)
	}
	if captured.Model != "" {
		t.Errorf("model = %q, want empty (omitted) when unconfigured", captured.Model)
	}
}

func TestOpenAIAskModelPassthrough(t *testing.T) {
	var captured chatRequest
	srv := serveChat(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`, &captured, nil)
	defer srv.Close()

	c, err := New(Config{Type: "openai", URL: srv.URL, Model: "gpt-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Ask(context.Background(), "s", "hi"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if captured.Model != "gpt-test" {
		t.Errorf("model = %q, want gpt-test", captured.Model)
	}
}

func TestOpenAIAskNoTokenHeader(t *testing.T) {
	var headers http.Header
	srv := serveChat(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`, nil, &headers)
	defer srv.Close()

	c, err := New(Config{Type: "openai", URL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Ask(context.Background(), "s", "hi"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, ok := headers["Authorization"]; ok {
		t.Error("Authorization header must be absent when no token is configured")
	}
}

func TestOpenAIAskErrorBody(t *testing.T) {
	srv := serveChat(t, http.StatusUnauthorized, `{"error":{"message":"bad key","type":"auth"}}`, nil, nil)
	defer srv.Close()

	c, _ := New(Config{Type: "openai", URL: srv.URL, Token: "wrong"})
	_, err := c.Ask(context.Background(), "s", "hi")
	if err == nil {
		t.Fatal("Ask must fail on HTTP 401")
	}
	if !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("error = %q, want it to carry status and server message", err)
	}
}

func TestOpenAIAskEmptyChoices(t *testing.T) {
	srv := serveChat(t, http.StatusOK, `{"choices":[]}`, nil, nil)
	defer srv.Close()

	c, _ := New(Config{Type: "openai", URL: srv.URL})
	if _, err := c.Ask(context.Background(), "s", "hi"); err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Errorf("error = %v, want empty response", err)
	}
}

func TestOpenAINeedsURL(t *testing.T) {
	if _, err := New(Config{Type: "openai"}); err == nil {
		t.Fatal("New without url must fail")
	}
}

// truncate must cut on rune boundaries — error strings leak into mesh
// messages, and a split rune would produce invalid UTF-8.
func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"ab", 2, "ab"},
		{"abc", 2, "ab…"},
		{"hello世界", 5, "hello…"},      // max lands on a rune start
		{"hello世界", 6, "hello…"},      // max lands mid-rune: back up
		{"привет мир", 12, "привет…"}, // multi-byte: cut lands on the space
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if !utf8.ValidString(truncate("привет мир, как дела сегодня", 10)) {
			t.Error("truncated string must stay valid UTF-8")
		}
	}
}
