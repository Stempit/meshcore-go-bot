package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	meshcore "meshbot/internal/meshcore_bt"
)

func main() {
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

	if false {
		// ============================
		// отправка сообщения контакту
		// ============================

		contacts := client.GetContacts()

		for _, v := range contacts {
			if v.Name == "Короед" {
				// отправка с ретраями до подтверждения отправки
				err := client.SendMessageWithRetry(v, "Пинг А", false, 5, 5*time.Second)
				if err != nil {
					fmt.Printf("Ошибка: %v\n", err)
				} else {
					fmt.Println("Сообщение отправлено и подтверждено")
				}

				// отправка с ретраями до подтверждения доставки
				err = client.SendMessageWithRetry(v, "Пинг Б", true, 5, 5*time.Second)
				if err != nil {
					fmt.Printf("Ошибка: %v\n", err)
				} else {
					fmt.Println("Сообщение отправлено и доставлено")
				}
			}
		}
	}

	if false {
		// ============================
		// отправка сообщения в канал
		// ============================

		channels := client.GetChannels()
		if ch, ok := channels[0]; ok && !ch.IsEmpty {
			err := client.SendChannelMessageWithRetry(ch, "ping С", false, 3, 5*time.Second)
			if err != nil {
				fmt.Printf("Ошибка: %v\n", err)
			} else {
				fmt.Println("Сообщение отправлено в канал и подтверждено")
			}
		} else {
			fmt.Println("Channel not found or empty")
		}
	}

	if false {
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

					// ответ без подтверждения
					err := client.SendMessageWithRetry(contact, "Ответ без подтверждения доставки!", false, 5, 5*time.Second)
					if err != nil {
						fmt.Printf("Ошибка: %v\n", err)
					} else {
						fmt.Println("Сообщение отправлено и подтверждено")
					}

					// ответ с подтверждением
					err = client.SendMessageWithRetry(contact, "Ответ с подтверждением доставки!", true, 5, 5*time.Second)
					if err != nil {
						fmt.Printf("Ошибка: %v\n", err)
					} else {
						fmt.Println("Сообщение отправлено и доставлено")
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

					// отправляем ответ
					err := client.SendChannelMessageWithRetry(channel, fmt.Sprintf("@[%s] ack", m.SenderName), false, 5, 5*time.Second)
					if err != nil {
						fmt.Printf("Ошибка: %v\n", err)
					} else {
						fmt.Println("Сообщение в канал отправлено")
					}
				}
			}
		}()

		// Блокируемся, пока не получим сигнал завершения
		fmt.Println("Waiting for messages... Press Ctrl+C to exit.")
		done := make(chan struct{}) // Канал для сигнала о завершении (например, при нажатии Ctrl+C)
		<-done
	}
}
