package llm

// =============
// Test version!
// =============

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"github.com/ollama/ollama/api"
)

var (
	FALSE = false
	TRUE  = true
)

func GetLocalLLMResponse(model string, systemPrompt string, temperature float64, request string) (response string, err error) {
	ctx := context.Background()

	var ollamaRawUrl string
	if ollamaRawUrl = os.Getenv("OLLAMA_HOST"); ollamaRawUrl == "" {
		ollamaRawUrl = "http://localhost:11434"
	}

	url, _ := url.Parse(ollamaRawUrl)

	client := api.NewClient(url, http.DefaultClient)

	req := &api.GenerateRequest{
		Model:  model,
		Prompt: request,
		System: systemPrompt,
		Options: map[string]interface{}{
			"temperature": temperature,
			// "repeat_last_n": 0,
			// "num_predict":   num_predict, // максимум токенов
			// "top_k":         10,
			// "top_p":         0.5,
		},
		Stream: &FALSE,
	}

	err = client.Generate(ctx, req, func(resp api.GenerateResponse) error {
		response = resp.Response
		return nil
	})

	if err != nil {
		slog.Error("LLM generate error", "error", fmt.Sprintf("%s", err))

	}
	return response, err
}
