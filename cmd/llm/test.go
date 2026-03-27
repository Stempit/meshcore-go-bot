package main

import (
	"fmt"
	"log"
	"log/slog"
	"meshbot/internal/commands"

	"meshbot/internal/config"
)

// используется для отладки промптов и других параметров LLM
func main() {

	err := config.Load("config.yaml")
	if err != nil {
		// Обрабатываем ошибку: можно завершить приложение с подробным сообщением
		slog.Error("Failed to load configuration", "error", err)
		log.Fatal("Cannot start application: configuration error")
	}

	fmt.Println(commands.ProcessLLMMessage(".llm круче Кант или Гегель?"))
	fmt.Println(commands.ProcessLLMMessage(".llm как определить съедобный гриб?"))
	fmt.Println(commands.ProcessLLMMessage(".llm как разжечь огонь без спичек?"))
	fmt.Println(commands.ProcessLLMMessage(".llm кто победит - майк тайсон или джеки чан?"))
}
