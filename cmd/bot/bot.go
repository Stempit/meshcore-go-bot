package main

import (
	"fmt"
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
		os.Exit(1)
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

	// log history in client
	client.SetHistoryLogger(histlog)

	// mqtt init
	mqttCfg := config.Get().MQTT
	client.SetMQTTConfig(&mqttCfg)
	client.RequestAllStats()

	// =================================
	// Синхронизация сохранённых сообщений
	// =================================

	syncDone := make(chan struct{})
	client.OnNoMoreMessages(func() {
		for _, m := range client.GetAllMessages() {
			slog.Info("[Stored] Личное", "sender", fmt.Sprintf("%x", m.SenderKey[:6]), "text", m.Text)
		}
		for _, m := range client.GetAllChannelMessages() {
			slog.Info("[Stored] Канал", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)
		}
		close(syncDone)
	})

	if err := client.SyncNextMessage(); err != nil {
		slog.Error("Failed to sync stored messages", "error", err)
		close(syncDone)
	}
	<-syncDone

	// Сбрасываем буфер канала — там могут быть сохранённые сообщения
	for {
		select {
		case <-client.IncomingMessages():
		default:
			goto startProcessing
		}
	}
startProcessing:

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

				slog.Info("Личное сообщение", "sender", fmt.Sprintf("%x", m.SenderKey[:6]), "text", m.Text)

				// собираем ответы
				replies, cmdErr := commands.GenerateReplies(m.Text, "")
				if cmdErr != nil {
					slog.Error("Ошибка команды", "error", cmdErr)
					continue
				}
				if len(replies) == 0 {
					slog.Info("Нет ответа для сообщения, пропускаем")
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
						slog.Error("Ошибка отправки", "error", err)
					} else {
						slog.Info("Сообщение отправлено")
					}
				}
			case *meshcore.ChannelMessage: // сообщения в каналах

				slog.Info("Канальное сообщение", "channel", m.ChannelIndex, "sender", m.SenderName, "text", m.Text)

				// находим канал по номеру
				channels := client.GetChannels()
				channel, ok := channels[m.ChannelIndex]
				if !ok || channel.IsEmpty {
					slog.Warn("Канал не найден или пуст", "channel", m.ChannelIndex)
					continue
				}

				// собираем ответы
				replies, cmdErr := commands.GenerateReplies(m.Text, channel.Name) //todo: может channelName выпилить, используется только для .last, можно принудительно указывать в запросе канал
				if cmdErr != nil {
					slog.Error("Ошибка команды", "error", cmdErr)
					continue
				}
				if len(replies) == 0 {
					slog.Debug("Не найден ответ, пропускаем")
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
						slog.Error("Ошибка отправки в канал", "error", err)
					} else {
						slog.Info("Сообщение в канал отправлено")
					}
				}
			}
		}
	}()

	// Блокируемся, пока не получим сигнал завершения
	slog.Info("Waiting for messages... Press Ctrl+C to exit.")
	done := make(chan struct{}) // Канал для сигнала о завершении (например, при нажатии Ctrl+C)

	time.Sleep(10 * time.Second)
	channels := client.GetChannels()
	if ch, ok := channels[3]; ok && !ch.IsEmpty {
		err := client.SendChannelMessageWithRetry(ch, "ping", false, 3, 5*time.Second)
		if err != nil {
			slog.Error("Ошибка отправки в канал", "error", err)
		} else {
			slog.Info("Сообщение отправлено в канал и подтверждено")
		}
	} else {
		slog.Warn("Channel not found or empty")
	}

	contacts := client.GetContacts()

	for _, v := range contacts {
		if v.Name == "Короед" {
			// отправка с ретраями до подтверждения отправки
			// err := client.SendMessageWithRetry(v, "Пинг А", false, 5, 5*time.Second)
			// if err != nil {
			// 	slog.Error("Ошибка", "error", err)
			// } else {
			// 	slog.Info("Сообщение отправлено")
			// }

			// отправка с ретраями до подтверждения доставки
			err = client.SendMessageWithRetry(v, "ping", true, 5, 5*time.Second)
			if err != nil {
				slog.Error("Ошибка отправки", "error", err)
			} else {
				slog.Info("Сообщение отправлено и доставлено")
			}
		}
	}

	// =================================
	// Запрашиваем и показываем статистику
	// =================================
	time.Sleep(2 * time.Second)
	if err := client.RequestAllStats(); err != nil {
		slog.Error("Failed to request stats", "error", err)
	} else {
		// Ждём, пока придут все stats
		time.Sleep(500 * time.Millisecond)
		fmt.Println(client.FormatStats())
	}

	// Периодически запрашиваем статистику для обновления MQTT
	go func() {
		ticker := time.NewTicker(30 * time.Second) // Запрашиваем каждые 30 секунд
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := client.RequestAllStats(); err != nil {
					slog.Error("Failed to request periodic stats", "error", err)
				}
			case <-done:
				return
			}
		}
	}()

	<-done
}
