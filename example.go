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

	contacts := client.GetContacts()

	for _, v := range contacts {
		if v.Name == "Короед" {
			// if err := client.SendMessage(v, "Далеко ли до Таллина?"); err != nil {
			// 	fmt.Printf("Failed to send message: %v\n", err)
			// } else {
			// 	fmt.Println("Message sent")
			// }
			err := client.SendMessageWithRetry(v, "Ответь текстом 'ввв'!", false, 5, 5*time.Second)
			if err != nil {
				fmt.Printf("Ошибка: %v\n", err)
			} else {
				fmt.Println("Сообщение отправлено и подтверждено")
			}
			time.Sleep(5000 * time.Millisecond)
			err = client.SendMessageWithRetry(v, "Ответь текстом 'ггг'!", true, 15, 10*time.Second)
			if err != nil {
				fmt.Printf("Ошибка: %v\n", err)
			} else {
				fmt.Println("Сообщение отправлено и доставлено")
			}
		}
		//fmt.Printf("%v+", v)
	}

	fmt.Println("====")
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
}
