package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ollama/ollama/api"
)

// localTimeout bounds one ollama generate call. The shrink compressor runs
// synchronously inside a dispatcher worker while holding the sender's
// in-flight slot: an unbounded call against a hung local LLM would hold both
// forever (the remote openrouter path is bounded by its HTTP client timeout).
const localTimeout = 90 * time.Second

// localClient has its own timeout instead of http.DefaultClient, which none.
var localClient = &http.Client{Timeout: localTimeout}

func GetLocalLLMResponse(model, systemPrompt string, temperature float64, request string) (string, error) {
	rawURL := os.Getenv("OLLAMA_HOST")
	if rawURL == "" {
		rawURL = "http://localhost:11434"
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("ollama: invalid host URL: %w", err)
	}

	client := api.NewClient(u, localClient)

	stream := false
	req := &api.GenerateRequest{
		Model:  model,
		Prompt: request,
		System: systemPrompt,
		Options: map[string]interface{}{
			"temperature": temperature,
		},
		Stream: &stream,
	}

	ctx, cancel := context.WithTimeout(context.Background(), localTimeout)
	defer cancel()

	var response string
	err = client.Generate(ctx, req, func(resp api.GenerateResponse) error {
		response = resp.Response
		return nil
	})
	if err != nil {
		log().Error("LLM generate error", "error", err)
		return "", err
	}
	return response, nil
}
