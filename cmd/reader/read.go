package main

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
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

	// Канал для сигнала окончания синхронизации
	syncDone := make(chan struct{})
	var mu sync.Mutex
	syncActive := false

	// Обработка личных сообщений
	client.OnMessage(func(msg *meshcore.Message) {
		// Выводим сообщение
		fmt.Printf("[DM] %s: %s\n", msg.SenderKey[:6], msg.Text)

		// Если синхронизация активна, запрашиваем следующее сообщение
		mu.Lock()
		active := syncActive
		mu.Unlock()
		if active {
			if err := client.SyncNextMessage(); err != nil {
				fmt.Printf("SyncNextMessage error: %v\n", err)
			}
		}
	})

	// Обработка канальных сообщений
	client.OnChannelMessage(func(msg *meshcore.ChannelMessage) {
		fmt.Printf("[Channel %d] %s: %s\n", msg.ChannelIndex, msg.SenderName, msg.Text)

		mu.Lock()
		active := syncActive
		mu.Unlock()
		if active {
			if err := client.SyncNextMessage(); err != nil {
				fmt.Printf("SyncNextMessage error: %v\n", err)
			}
		}
	})

	// Обработка окончания очереди
	client.OnNoMoreMessages(func() {
		mu.Lock()
		if syncActive {
			syncActive = false
			close(syncDone)
		}
		mu.Unlock()
		fmt.Println("No more messages in queue.")
	})

	// (Опционально) Загружаем контакты и каналы, чтобы имена отображались
	if err := client.RequestChannels(8); err != nil {
		fmt.Printf("RequestChannels error: %v\n", err)
	}
	if err := client.RequestContacts(0); err != nil {
		fmt.Printf("RequestContacts error: %v\n", err)
	}

	// Запускаем синхронизацию
	mu.Lock()
	syncActive = true
	mu.Unlock()
	fmt.Println("Starting message sync...")
	if err := client.SyncNextMessage(); err != nil {
		fmt.Printf("Initial SyncNextMessage error: %v\n", err)
		syncActive = false
		close(syncDone)
	}

	// Ждём завершения синхронизации (или таймаут на случай проблем)
	select {
	case <-syncDone:
		fmt.Println("Message sync completed.")
	case <-time.After(30 * time.Second):
		fmt.Println("Sync timeout, stopping.")
		mu.Lock()
		syncActive = false
		mu.Unlock()
	}

	fmt.Println("\n=== Stored messages ===")
	messages := client.GetAllMessages() // нужно реализовать такой метод
	for _, msg := range messages {
		fmt.Printf("[DM] %s: %s\n", msg.SenderKey[:6], msg.Text)

		fmt.Printf("%v+\n", msg)
	}
	channelMessages := client.GetAllChannelMessages()
	for _, msg := range channelMessages {
		fmt.Printf("[Channel %d] %s: %s\n", msg.ChannelIndex, msg.SenderName, msg.Text)
	}

	// После синхронизации можно продолжать слушать новые сообщения
	fmt.Println("Now listening for new messages...")
	select {} // блокировка, чтобы программа не завершилась
}
