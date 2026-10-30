package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxResponseBytes caps how much of the agent's response body is read. Agent
// answers go through shrink fitting later; anything near this cap is already
// unusable, and the cap stops a misbehaving server from pinning memory.
const maxResponseBytes = 1 << 20

// maxErrBytes caps the server message copied into error strings.
const maxErrBytes = 200

// openAI talks to an OpenAI-compatible chat completions API. Each request
// carries a single user message; history is managed by the server and
// selected via the session_id body field (the nanobot extension — servers
// that don't know it ignore the extra field). Non-streaming: SSE error
// reporting is unreliable across implementations, and mesh replies want the
// complete text anyway.
type openAI struct {
	base  string // base URL without trailing slash, e.g. http://127.0.0.1:8900
	token string
	model string
	http  *http.Client
}

func newOpenAI(cfg Config) (*openAI, error) {
	base := strings.TrimSuffix(cfg.URL, "/")
	if base == "" {
		return nil, errors.New(`agentclient: openai adapter needs url (e.g. "http://127.0.0.1:8900")`)
	}
	// No client Timeout: the per-call deadline comes from the caller's ctx
	// (agent answers can legitimately take minutes).
	return &openAI{base: base, token: cfg.Token, model: cfg.Model, http: &http.Client{}}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model,omitempty"`
	Messages  []chatMessage `json:"messages"`
	SessionID string        `json:"session_id"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *openAI) Ask(ctx context.Context, sessionID, text string) ([]string, error) {
	reqBody := chatRequest{
		Messages:  []chatMessage{{Role: "user", Content: text}},
		SessionID: sessionID,
	}
	if c.model != "" {
		reqBody.Model = c.model
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("openai: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, truncate(errorMessage(body), maxErrBytes))
	}

	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return nil, errors.New("openai: empty response")
	}
	return []string{out.Choices[0].Message.Content}, nil
}

// errorMessage extracts a human-readable message from an error response body:
// the message field of an {"error":{...}} JSON object when present, otherwise
// the raw body. Never include request credentials — only the server's text.
func errorMessage(body []byte) string {
	var env struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil && env.Error.Message != "" {
		return env.Error.Message
	}
	return strings.TrimSpace(string(body))
}

// truncate cuts s to at most max bytes without splitting a rune (the range
// loop yields rune start offsets), appending an ellipsis when something was
// dropped.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := 0
	for i := range s {
		if i > max {
			break
		}
		cut = i
	}
	return s[:cut] + "…"
}
