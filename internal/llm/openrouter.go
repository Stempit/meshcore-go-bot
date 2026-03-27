package llm

import (
	"context"
	"log"
	"os"

	"github.com/revrost/go-openrouter"
)

func GetRemoteLLMResponse(model string, systemPrompt string, temperature float64, request string) (response string, err error) {
	// Set your API key and initialize the client
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
		log.Fatal(err)
	}

	return resp.Choices[0].Message.Content.Text, err
}
