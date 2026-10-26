package main

import (
	"fmt"
	"github.com/stempit/meshcore-go-bot/internal/commands/llm"
	"github.com/stempit/meshcore-go-bot/internal/config"
	"github.com/stempit/meshcore-go-bot/internal/logging"
	"os"
)

// используется для отладки промптов и других параметров LLM
func main() {
	var log = logging.For("llm-test")
	err := config.Load("config.yaml")
	if err != nil {
		// Обрабатываем ошибку: можно завершить приложение с подробным сообщением
		log().Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	fmt.Println(llm.ProcessLLMMessage(".llm круче Кант или Гегель?"))
	fmt.Println(llm.ProcessLLMMessage(".llm как определить съедобный гриб?"))
	fmt.Println(llm.ProcessLLMMessage(".llm как разжечь огонь без спичек?"))
	fmt.Println(llm.ProcessLLMMessage(".llm кто победит - майк тайсон или джеки чан?"))
}
