package llm

import (
	"context"
	"fmt"
	"meshbot/internal/proxy"
	"os"

	"github.com/revrost/go-openrouter"
)

func GetRemoteLLMResponse(model, systemPrompt string, temperature float64, request string) (string, error) {
	cfg := openrouter.DefaultConfig(os.Getenv("OPENROUTER_API_KEY"))
	cfg.HTTPClient = proxy.HTTPClient()
	client := openrouter.NewClientWithConfig(*cfg)

	req := openrouter.ChatCompletionRequest{
		Model:       model,
		Temperature: float32(temperature),
		Messages: []openrouter.ChatCompletionMessage{
			openrouter.SystemMessage(systemPrompt),
			openrouter.UserMessage(request),
		},
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("openrouter: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("openrouter: empty response")
	}
	return resp.Choices[0].Message.Content.Text, nil
}
