package main

import (
	"fmt"
	"strings"
	"time"

	meshcore "meshbot/internal/meshcore_bt"

	"tinygo.org/x/bluetooth"
)

func main() {
	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		panic(err)
	}
	fmt.Println("Bluetooth enabled")

	addr, name, err := scanMeshCore(adapter)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Found device: %s (%s)\n", name, addr.String())

	client := meshcore.NewMeshCoreClient(adapter)

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

	// Подключаемся
	if err := client.Connect(addr); err != nil {
		panic(err)
	}
	defer client.Disconnect()

	// Запрашиваем список каналов (например, 8 каналов)
	if err := client.RequestChannels(3); err != nil {
		fmt.Printf("Request channels error: %v\n", err)
	}

	// Отправляем тестовое сообщение в канал 2, если он существует
	time.Sleep(2 * time.Second) // Ждём загрузки каналов
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

func scanMeshCore(adapter *bluetooth.Adapter) (bluetooth.Address, string, error) {
	resultChan := make(chan bluetooth.ScanResult)
	err := adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		if strings.HasPrefix(result.LocalName(), "MeshCore-") {
			adapter.StopScan()
			resultChan <- result
		}
	})
	if err != nil {
		return bluetooth.Address{}, "", err
	}
	select {
	case res := <-resultChan:
		return res.Address, res.LocalName(), nil
	case <-time.After(10 * time.Second):
		adapter.StopScan()
		return bluetooth.Address{}, "", fmt.Errorf("scan timeout")
	}
}
