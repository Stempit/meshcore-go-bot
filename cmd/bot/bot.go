package main

import (
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"meshbot/internal/commands"
	"meshbot/internal/config"
	meshcore "meshbot/internal/meshcore_bt"
)

func main() {

	// config
	err := config.Load("config.yaml")
	if err != nil {
		// Обрабатываем ошибку: можно завершить приложение с подробным сообщением
		slog.Error("Failed to load configuration", "error", err)
		log.Fatal("Cannot start application: configuration error")
	}

	// logging
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	handler := slog.NewTextHandler(os.Stdout, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// meshcore client
	client := meshcore.CreateClient()
	defer client.Disconnect()

	client.SetMQTTConfig(&meshcore.MQTTConfig{
		Server:         "meshcoretel.ru",
		Port:           1883,
		Username:       "meshcore",
		Password:       "meshcore",
		Origin:         "MeshCore-7C44431A",
		IATA:           "KVX",
		StatusEnabled:  true,
		PacketsEnabled: true,
		StatusInterval: 30000,
		AnalyzerEU:     true,
		OwnerEmail:     "no@mail.me",
		// Ed25519 private key (64 hex chars) - REQUIRED for Let's Mesh Analyzer JWT signing
		// Replace with your actual device private key
		DevicePrivKey: "c88339c95e245316303d9ca77f7c0d296affe1f74a974ec10c6cf5649ba444427373a5297fdf8e961cf4db3a6e4c871b56bcc1e65333da83721116973bc7ddb3",
	})
	// =================================
	// Обработка входящих сообщений
	// =================================

	go func() {
		// слушаем поступающие сообщения
		for msg := range client.IncomingMessages() {

			// в зависимости от - личка или канал
			switch m := msg.(type) {
			case *meshcore.Message: // личные сообщения

				// Находим контакт отправителя по ключу
				senderKeyHex := fmt.Sprintf("%x", m.SenderKey[:6]) // можно использовать префикс
				var contact *meshcore.Contact
				for _, c := range client.GetContacts() {
					if fmt.Sprintf("%x", c.PublicKey[:6]) == senderKeyHex {
						contact = c
						break
					}
				}
				if contact == nil {
					// Если контакт не найден, создаём временный (не сохраняем)
					contact = &meshcore.Contact{
						PublicKey: m.SenderKey,
						Name:      fmt.Sprintf("?%x", m.SenderKey[:6]),
					}
				}

				fmt.Printf("Личное сообщение от %x: %s\n", m.SenderKey[:6], m.Text)
				fmt.Printf("Отвечаем")

				replyText := commands.GenerateReply(m.Text)

				// если не отвечаем
				if replyText == "" {
					fmt.Printf("Не найден ответ")
					continue
				}

				// ответ с подтверждением
				err = client.SendMessageWithRetry(contact, replyText, true, 5, 5*time.Second)
				if err != nil {
					fmt.Printf("Ошибка: %v\n", err)
				} else {
					fmt.Println("Сообщение отправлено и доставлено")
				}

				// если не смогли с подтверждением - доставляем абы как
				if err != nil {
					// ответ без подтверждения
					err := client.SendMessageWithRetry(contact, replyText, false, 5, 5*time.Second)
					if err != nil {
						fmt.Printf("Ошибка: %v\n", err)
					} else {
						fmt.Println("Сообщение отправлено и подтверждено")
					}
				}
			case *meshcore.ChannelMessage: // сообщения в каналах

				fmt.Printf("Канальное сообщение в канале %d от %s: %s\n", m.ChannelIndex, m.SenderName, m.Text)

				// находим канал по номеру
				channels := client.GetChannels()
				channel, ok := channels[m.ChannelIndex]
				if !ok || channel.IsEmpty {
					fmt.Printf("Канал %d не найден или пуст\n", m.ChannelIndex)
					continue
				}

				replyText := commands.GenerateReply(m.Text)

				// если не отвечаем
				if replyText == "" {
					fmt.Printf("Не найден ответ")
					continue
				}

				// отправляем ответ
				err := client.SendChannelMessageWithRetry(channel, fmt.Sprintf("@[%s] %s", m.SenderName, replyText), false, 5, 5*time.Second)
				if err != nil {
					fmt.Printf("Ошибка: %v\n", err)
				} else {
					fmt.Println("Сообщение в канал отправлено")
				}
			}
		}
	}()

	// Блокируемся, пока не получим сигнал завершения
	fmt.Println("\nWaiting for messages... Press Ctrl+C to exit.")

	// =================================
	// Запрашиваем и показываем статистику
	// =================================
	time.Sleep(2 * time.Second)
	if err := client.RequestAllStats(); err != nil {
		fmt.Printf("Failed to request stats: %v\n", err)
	} else {
		// Ждём, пока придут все stats
		time.Sleep(500 * time.Millisecond)
		fmt.Println(client.FormatStats())
	}

	done := make(chan struct{}) // Канал для сигнала о завершении (например, при нажатии Ctrl+C)
	<-done
}
