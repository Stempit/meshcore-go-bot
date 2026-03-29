package main

import (
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"meshbot/internal/commands"
	"meshbot/internal/config"
	"meshbot/internal/historylog"
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

	// history logger
	histlog := historylog.New("log/history")
	defer histlog.Close()

	// meshcore client
	client := meshcore.CreateClient()
	defer client.Disconnect()

	client.SetHistoryLogger(histlog)

	mqttCfg := config.Get().MQTT
	client.SetMQTTConfig(&mqttCfg)
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

				// собираем ответы
				replies, cmdErr := commands.GenerateReplies(m.Text, "")
				if cmdErr != nil {
					fmt.Printf("Ошибка команды: %v\n", cmdErr)
					continue
				}
				if len(replies) == 0 {
					fmt.Printf("Не найден ответ\n")
					continue
				}

				// отправляем ответы; при нескольких делаем паузу между сообщениями
				for i, replyText := range replies {
					if i > 0 {
						time.Sleep(5 * time.Second)
					}
					// пытаемся отправить с подтверждением
					err = client.SendMessageWithRetry(contact, replyText, true, 5, 5*time.Second)
					if err != nil {
						// если не получилось - отправляем хотя бы без подтверждения
						err = client.SendMessageWithRetry(contact, replyText, false, 5, 5*time.Second)
					}
					if err != nil {
						fmt.Printf("Ошибка: %v\n", err)
					} else {
						fmt.Println("Сообщение отправлено")
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

				// собираем ответы
				replies, cmdErr := commands.GenerateReplies(m.Text, channel.Name) //todo: может channelName выпилить, используется только для .last, можно принудительно указывать в запросе канал
				if cmdErr != nil {
					fmt.Printf("Ошибка команды: %v\n", cmdErr)
					continue
				}
				if len(replies) == 0 {
					fmt.Printf("Не найден ответ\n")
					continue
				}

				// отправляем ответы; при нескольких делаем паузу между сообщениями
				for i, replyText := range replies {
					if i > 0 {
						time.Sleep(5 * time.Second)
					}
					text := replyText
					if len(replies) == 1 {
						text = fmt.Sprintf("@[%s] %s", m.SenderName, replyText)
					}
					err := client.SendChannelMessageWithRetry(channel, text, false, 5, 5*time.Second)
					if err != nil {
						fmt.Printf("Ошибка отправки в канал: %v\n", err)
					} else {
						fmt.Println("Сообщение в канал отправлено")
					}
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

	time.Sleep(10 * time.Second)
	channels := client.GetChannels()
	if ch, ok := channels[3]; ok && !ch.IsEmpty {
		err := client.SendChannelMessageWithRetry(ch, "ping", false, 3, 5*time.Second)
		if err != nil {
			fmt.Printf("Ошибка: %v\n", err)
		} else {
			fmt.Println("Сообщение отправлено в канал и подтверждено")
		}
	} else {
		fmt.Println("Channel not found or empty")
	}

	contacts := client.GetContacts()

	for _, v := range contacts {
		if v.Name == "Короед" {
			// отправка с ретраями до подтверждения отправки
			// err := client.SendMessageWithRetry(v, "Пинг А", false, 5, 5*time.Second)
			// if err != nil {
			// 	fmt.Printf("Ошибка: %v\n", err)
			// } else {
			// 	fmt.Println("Сообщение отправлено и подтверждено")
			// }

			// отправка с ретраями до подтверждения доставки
			err = client.SendMessageWithRetry(v, "ping", true, 5, 5*time.Second)
			if err != nil {
				fmt.Printf("Ошибка: %v\n", err)
			} else {
				fmt.Println("Сообщение отправлено и доставлено")
			}
		}
	}

	// Периодически запрашиваем статистику для обновления MQTT
	go func() {
		ticker := time.NewTicker(30 * time.Second) // Запрашиваем каждые 30 секунд
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := client.RequestAllStats(); err != nil {
					fmt.Printf("Failed to request periodic stats: %v\n", err)
				}
			case <-done:
				return
			}
		}
	}()

	<-done
}
