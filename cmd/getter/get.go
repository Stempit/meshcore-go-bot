package main

import (
	"fmt"
	"log/slog"
	"os"

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

	// Канал для сигнала о завершении (например, при нажатии Ctrl+C)
	done := make(chan struct{})

	// Обработка личных сообщений
	client.OnMessage(func(msg *meshcore.Message) {
		senderName := "Unknown"
		contacts := client.GetContacts()
		if contact, ok := contacts[fmt.Sprintf("%x", msg.SenderKey)]; ok {
			senderName = contact.Name
		}
		if len(msg.SenderKey) >= 6 {
			fmt.Printf("[DM] %s (%x): %s\n", senderName, msg.SenderKey[:6], msg.Text)
		} else {
			fmt.Printf("[DM] %s (unknown): %s\n", senderName, msg.Text)
		}
	})

	// Обработка сообщений в каналах
	client.OnChannelMessage(func(msg *meshcore.ChannelMessage) {
		fmt.Printf("[Channel %d] %s: %s\n", msg.ChannelIndex, msg.SenderName, msg.Text)
	})

	// Дополнительные коллбэки для отладки
	client.OnSelfInfo(func(info *meshcore.SelfInfo) {
		fmt.Printf("Self info: %s\n", info.Name)
	})

	client.OnContact(func(contact *meshcore.Contact) {
		fmt.Printf("Contact: %s (%x)\n", contact.Name, contact.PublicKey[:6])
	})

	client.OnChannel(func(ch *meshcore.Channel) {
		if !ch.IsEmpty {
			fmt.Printf("Channel %d: %s\n", ch.Index, ch.Name)
		}
	})

	// channels := client.GetChannels()
	// if ch, ok := channels[2]; ok && !ch.IsEmpty {
	// 	fmt.Printf("Sending 'ping' to channel %d (%s)\n", ch.Index, ch.Name)
	// 	if err := client.SendChannelMessage(ch, "ping"); err != nil {
	// 		fmt.Printf("Send error: %v\n", err)
	// 	} else {
	// 		fmt.Println("Message sent")
	// 	}
	// }

	// Блокируемся, пока не получим сигнал завершения
	fmt.Println("Waiting for messages... Press Ctrl+C to exit.")
	<-done
}
