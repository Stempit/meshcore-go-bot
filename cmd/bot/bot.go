package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"strings"
	"sync"

	"meshbot/internal/commands"
	"meshbot/internal/config"
	"meshbot/internal/historylog"
	meshcore "meshbot/internal/meshcore_bt"
	"meshbot/internal/rss"
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
	// Приветствие новых узлов
	// =================================

	knownKeys, err := commands.NewKnownKeys("log/known.txt")
	if err != nil {
		slog.Error("Failed to load known keys", "error", err)
		os.Exit(1)
	}

	sendGreeting := func(contact *meshcore.Contact) {
		slog.Info("New node seen, sending greeting", "name", contact.Name, "type", contact.Type)
		pubCh := client.GetChannels()[0]
		if pubCh == nil || pubCh.IsEmpty {
			slog.Warn("Public channel 0 not available, skipping greeting")
			return
		}
		msg := commands.GreetMessage(contact.Name, contact.PublicKey, contact.Type)
		if err := client.SendChannelMessageWithRetry(pubCh, msg, false, 3, 5*time.Second); err != nil {
			slog.Error("Failed to send greeting", "error", err)
		}
	}

	// pendingGreets holds public-key hex strings of nodes whose advert arrived
	// before their contact record was in the cache. They will be greeted once
	// the full contact is received via OnContact.
	pendingGreets := make(map[string]struct{})
	var pendingMu sync.Mutex

	client.OnAdvert(func(contact *meshcore.Contact) {
		alreadyKnown, err := knownKeys.CheckAndAdd(contact.PublicKey, contact.Name)
		if err != nil {
			slog.Error("Failed to update known keys", "error", err)
			return
		}
		if alreadyKnown {
			return
		}
		if contact.Type == 0 && contact.Name == "" {
			// Stub contact: type/name unknown yet. Request a contacts refresh and
			// defer the greeting until the full record arrives via OnContact.
			slog.Info("New node seen (stub), deferring greeting until contact info arrives",
				"pub_key", fmt.Sprintf("%x", contact.PublicKey))
			keyHex := fmt.Sprintf("%x", contact.PublicKey)
			pendingMu.Lock()
			pendingGreets[keyHex] = struct{}{}
			pendingMu.Unlock()
			if err := client.RequestContacts(0); err != nil {
				slog.Error("Failed to request contacts after stub advert", "error", err)
			}
			return
		}
		sendGreeting(contact)
	})

	client.OnContact(func(contact *meshcore.Contact) {
		keyHex := fmt.Sprintf("%x", contact.PublicKey)
		pendingMu.Lock()
		_, isPending := pendingGreets[keyHex]
		if isPending {
			delete(pendingGreets, keyHex)
		}
		pendingMu.Unlock()
		if !isPending {
			return
		}
		// This contact arrived as a stub advert earlier; greet it now that we
		// have its full record (name, type).
		alreadyKnown, err := knownKeys.CheckAndAdd(contact.PublicKey, contact.Name)
		if err != nil {
			slog.Error("Failed to update known keys", "error", err)
			return
		}
		if alreadyKnown {
			return
		}
		sendGreeting(contact)
	})

	// =================================
	// Пересылка сообщений из телеги
	// =================================

	var tgb *tgbridge.Bridge
	if tgCfg := config.Get().Telegram; tgCfg.Enabled {
		tgb, err = tgbridge.New(tgCfg, logger)
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

	// =================================
	// RSS poller
	// =================================

	if rssCfg := config.Get().RSS; rssCfg.Enabled {
		var feeds []rss.Feed
		for _, f := range rssCfg.Feeds {
			dur, err := time.ParseDuration(f.Interval)
			if err != nil {
				slog.Error("RSS: invalid interval, skipping feed", "url", f.URL, "interval", f.Interval, "error", err)
				continue
			}
			feeds = append(feeds, rss.Feed{
				URL:      f.URL,
				Channel:  f.Channel,
				Prefix:   f.Prefix,
				Interval: dur,
			})
		}
		seenFile := rssCfg.SeenFile
		if seenFile == "" {
			seenFile = "log/rss_seen.txt"
		}
		poller := rss.New(
			feeds,
			rssCfg.LongTitle,
			seenFile,
			func(channelName, text string) error {
				for _, ch := range client.GetChannels() {
					if ch.Name == channelName {
						return client.SendChannelMessageWithRetry(ch, text, false, 3, 5*time.Second)
					}
				}
				slog.Warn("RSS: meshcore channel not found", "name", channelName)
				return nil
			},
			commands.ShrinkText,
		)
		poller.Start()
	}

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

				// Фильтрация по whitelist/blacklist
				senderKeyHex := fmt.Sprintf("%x", m.SenderKey)
				if !config.Get().Security.Direct.IsAllowed(senderKeyHex) {
					slog.Info("Direct message filtered by security policy", "sender", senderKeyHex[:12])
					continue
				}

				// Находим контакт отправителя по ключу
				senderKeyHex = fmt.Sprintf("%x", m.SenderKey[:6]) // можно использовать префикс
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

				// техническая информация для пингов
				pingMeta := &commands.PingMeta{
					PathLength: m.PathLength,
					PathBytes:  m.PathBytes,
					Timestamp:  m.Timestamp,
					SenderLat:  contact.Latitude,
					SenderLon:  contact.Longitude,
				}
				setSelfCoords(pingMeta, client)
				if hops := m.PathLength; hops > 0 && len(m.PathBytes) > 0 {
					var bph int
					if len(m.PathBytes) == hops {
						bph = 1
					} else if len(m.PathBytes) == hops*2 {
						bph = 2
					}
					if bph > 0 {
						pingMeta.HopCoords, pingMeta.HopKnown = resolveHopCoords(m.PathBytes, hops, bph, client.GetContacts())
					}
				}

				// собираем ответы
				replies, cmdErr := commands.GenerateReplies(m.Text, "", pingMeta)
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

				// Фильтрация по whitelist/blacklist
				if !config.Get().Security.Channels.IsAllowed(channel.Name) {
					slog.Info("Channel message filtered by security policy", "channel", channel.Name)
					continue
				}

				// Пересылка в Telegram
				if tgb != nil {
					tgb.Forward(channel.Name, m.SenderName, m.Text)
				}

				// техническая информация для пингов
				pingMeta := &commands.PingMeta{
					PathLength: m.PathLength,
					PathBytes:  m.PathBytes,
					Timestamp:  m.Timestamp,
				}
				for _, c := range client.GetContacts() {
					if c.Name == m.SenderName {
						pingMeta.SenderLat = c.Latitude
						pingMeta.SenderLon = c.Longitude
						break
					}
				}
				setSelfCoords(pingMeta, client)
				if hops := m.PathLength; hops > 0 && len(m.PathBytes) > 0 {
					var bph int
					if len(m.PathBytes) == hops {
						bph = 1
					} else if len(m.PathBytes) == hops*2 {
						bph = 2
					}
					if bph > 0 {
						pingMeta.HopCoords, pingMeta.HopKnown = resolveHopCoords(m.PathBytes, hops, bph, client.GetContacts())
					}
				}

				// собираем ответы
				replies, cmdErr := commands.GenerateReplies(m.Text, channel.Name, pingMeta) //todo: может channelName выпилить, используется только для .last, можно принудительно указывать в запросе канал
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
					if !client.IsConnected() {
						slog.Warn("BLE connection lost, reconnecting...")
						time.Sleep(5 * time.Second)
						if err := client.Reconnect(); err != nil {
							slog.Error("Reconnect failed", "error", err)
						} else {
							slog.Info("Reconnected to device")
						}
					}
				}
			case <-done:
				return
			}
		}
	}()

	<-done
}

// setSelfCoords fills pingMeta.SelfLat/SelfLon from config (when use_config_coords is true)
// or from the device's own SelfInfo. Config coords take priority when the flag is set.
func setSelfCoords(pingMeta *commands.PingMeta, client interface {
	GetSelfInfo() *meshcore.SelfInfo
}) {
	pingCfg := config.Get().Trace
	if pingCfg.UseConfigCoords {
		pingMeta.SelfLat = pingCfg.Lat
		pingMeta.SelfLon = pingCfg.Lon
		return
	}
	if self := client.GetSelfInfo(); self != nil {
		pingMeta.SelfLat = self.Latitude
		pingMeta.SelfLon = self.Longitude
	}
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

// resolveHopCoords resolves GPS coordinates for each intermediate hop in pathBytes.
// bytesPerHop is 1 or 2; each hop's bytes are matched against contact public key prefixes.
func resolveHopCoords(pathBytes []byte, hopCount, bytesPerHop int, contacts map[string]*meshcore.Contact) (coords [][2]float64, known []bool) {
	coords = make([][2]float64, hopCount)
	known = make([]bool, hopCount)
	for i := 0; i < hopCount; i++ {
		chunk := pathBytes[i*bytesPerHop : (i+1)*bytesPerHop]
		for _, c := range contacts {
			if len(c.PublicKey) >= bytesPerHop && bytesEqual(c.PublicKey[:bytesPerHop], chunk) {
				if c.Latitude != nil && c.Longitude != nil {
					coords[i] = [2]float64{*c.Latitude, *c.Longitude}
					known[i] = true
				}
				break
			}
		}
	}
	return
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
