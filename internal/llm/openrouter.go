package llm

import (
	"context"
	"fmt"
	"os"

	"github.com/revrost/go-openrouter"
)

func GetRemoteLLMResponse(model, systemPrompt string, temperature float64, request string) (string, error) {
	client := openrouter.NewClient(os.Getenv("OPENROUTER_API_KEY"))

	// Define and send the chat request
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
