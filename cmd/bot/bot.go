package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"strings"

	"meshbot/internal/commands"
	"meshbot/internal/config"
	"meshbot/internal/historylog"
	meshcore "meshbot/internal/meshcore_bt"
	"meshbot/internal/tgbridge"
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
	// Пересылка сообщений из телеги
	// =================================

	if tgCfg := config.Get().Telegram; tgCfg.Enabled {
		tgb, err := tgbridge.New(tgCfg, logger)
		if err != nil {
			slog.Error("Failed to start Telegram bridge", "error", err)
		} else {
			tgb.OnMessage(func(meshChannelName, senderName, text string, shrink bool) {
				var tgChannel *meshcore.Channel
				for _, ch := range client.GetChannels() {
					if ch.Name == meshChannelName {
						tgChannel = ch
						break
					}
				}
				if tgChannel == nil {
					slog.Warn("Telegram bridge: meshcore channel not found", "name", meshChannelName)
					return
				}
				prefix := fmt.Sprintf("[TG:%s] ", senderName)
				var parts []string
				if shrink {
					shrunk, err := commands.ShrinkText(text)
					if err != nil {
						slog.Warn("Telegram bridge: shrink failed, splitting instead", "error", err)
						parts = splitMeshMessage(prefix, text, 140)
					} else {
						parts = []string{prefix + shrunk}
					}
				} else {
					parts = splitMeshMessage(prefix, text, 140)
				}
				for i, part := range parts {
					if i > 0 {
						time.Sleep(3 * time.Second)
					}
					if err := client.SendChannelMessageWithRetry(tgChannel, part, false, 3, 5*time.Second); err != nil {
						slog.Error("Telegram bridge: failed to send to meshcore", "error", err)
					}
				}
			})
			go tgb.Start()
			defer tgb.Stop()
		}
	}

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
			// err = client.SendMessageWithRetry(v, "ping", true, 5, 5*time.Second)
			// if err != nil {
			// 	slog.Error("Ошибка отправки", "error", err)
			// } else {
			// 	slog.Info("Сообщение отправлено и доставлено")
			// }
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

// splitMeshMessage splits text into parts that each fit within maxLen bytes when prefixed.
// The prefix is applied to every part. Splits on word boundaries where possible.
func splitMeshMessage(prefix, text string, maxLen int) []string {
	budget := maxLen - len(prefix)
	if budget <= 0 {
		return []string{prefix}
	}
	if len(prefix)+len(text) <= maxLen {
		return []string{prefix + text}
	}

	var parts []string
	for len(text) > 0 {
		if len(text) <= budget {
			parts = append(parts, prefix+text)
			break
		}
		// find last space within budget
		cut := budget
		if i := strings.LastIndex(text[:cut], " "); i > 0 {
			cut = i
		}
		parts = append(parts, prefix+text[:cut])
		text = strings.TrimLeft(text[cut:], " ")
	}
	return parts
}
