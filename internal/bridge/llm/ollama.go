package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/ollama/ollama/api"
)

func GetLocalLLMResponse(model, systemPrompt string, temperature float64, request string) (string, error) {
	rawURL := os.Getenv("OLLAMA_HOST")
	if rawURL == "" {
		rawURL = "http://localhost:11434"
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("ollama: invalid host URL: %w", err)
	}

	client := api.NewClient(u, http.DefaultClient)

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

	var response string
	err = client.Generate(context.Background(), req, func(resp api.GenerateResponse) error {
		response = resp.Response
		return nil
	})
	if err != nil {
		log().Error("LLM generate error", "error", err)
		return "", err
	}
	return response, nil
}
