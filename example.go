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

	// actions

	//contacts := client.GetContacts()

	// for _, v := range contacts {
	// 	if v.Name == "Короед" {
	// 		// if err := client.SendMessage(v, "Далеко ли до Таллина?"); err != nil {
	// 		// 	fmt.Printf("Failed to send message: %v\n", err)
	// 		// } else {
	// 		// 	fmt.Println("Message sent")
	// 		// }
	// 		err := client.SendMessageWithRetry(v, "Ответь текстом 'ввв'!", false, 5, 5*time.Second)
	// 		if err != nil {
	// 			fmt.Printf("Ошибка: %v\n", err)
	// 		} else {
	// 			fmt.Println("Сообщение отправлено и подтверждено")
	// 		}
	// 		time.Sleep(3000 * time.Millisecond)
	// 		err = client.SendMessageWithRetry(v, "Ответь текстом 'ггг'!", true, 5, 5*time.Second)
	// 		if err != nil {
	// 			fmt.Printf("Ошибка: %v\n", err)
	// 		} else {
	// 			fmt.Println("Сообщение отправлено и доставлено")
	// 		}
	// 	}
	// 	//fmt.Printf("%v+", v)
	// }

	// Получаем список каналов
	channels := client.GetChannels()
	if ch, ok := channels[0]; ok && !ch.IsEmpty {
		err := client.SendChannelMessageWithRetry(ch, "ping H", false, 3, 5*time.Second)
		if err != nil {
			fmt.Printf("Ошибка: %v\n", err)
		} else {
			fmt.Println("Сообщение отправлено в канал и подтверждено")
		}

		// err = client.SendChannelMessageWithRetry(ch, "ping I", true, 3, 5*time.Second)
		// if err != nil {
		// 	fmt.Printf("Ошибка: %v\n", err)
		// } else {
		// 	fmt.Println("Сообщение отправлено в канал и доставлено")
		// }
	} else {
		fmt.Println("Channel not found or empty")
	}

	// // Получаем список каналов
	// channels := client.GetChannels()
	// if ch, ok := channels[0]; ok && !ch.IsEmpty {
	// 	fmt.Printf("Sending 'ping' to channel %d (%s)\n", ch.Index, ch.Name)
	// 	if err := client.SendChannelMessage(ch, "cli ping"); err != nil {
	// 		fmt.Printf("Failed to send message: %v\n", err)
	// 	} else {
	// 		fmt.Println("Message sent")
	// 	}
	// } else {
	// 	fmt.Println("Channel 2 not found or empty")
	// }

	go func() {
		for msg := range client.IncomingMessages() {
			switch m := msg.(type) {
			case *meshcore.Message:
				fmt.Printf("Личное сообщение от %x: %s\n", m.SenderKey[:6], m.Text)
			case *meshcore.ChannelMessage:
				fmt.Printf("Канальное сообщение в канале %d от %s: %s\n", m.ChannelIndex, m.SenderName, m.Text)
			}
		}
	}()
	// Блокируемся, пока не получим сигнал завершения
	fmt.Println("Waiting for messages... Press Ctrl+C to exit.")
	done := make(chan struct{}) // Канал для сигнала о завершении (например, при нажатии Ctrl+C)
	<-done
}
