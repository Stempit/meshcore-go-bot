package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	meshcore "meshbot/internal/meshcore_bt"

	"tinygo.org/x/bluetooth"
)

func main() {

	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}
	handler := slog.NewTextHandler(os.Stdout, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)

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

	// Канал для синхронизации получения SelfInfo
	selfInfoReceived := make(chan struct{})

	client.OnSelfInfo(func(info *meshcore.SelfInfo) {
		fmt.Printf("Self info received: name=%s, pubkey=%x\n", info.Name, info.PublicKey)
		close(selfInfoReceived)
	})

	client.OnChannel(func(ch *meshcore.Channel) {
		if ch.IsEmpty {
			fmt.Printf("Channel %d: (empty)\n", ch.Index)
		} else {
			fmt.Printf("Channel %d: %s\n", ch.Index, ch.Name)
		}
	})

	if err := client.Connect(addr); err != nil {
		panic(err)
	}
	defer client.Disconnect()

	fmt.Println("Connected to device, waiting for data...")

	time.Sleep(2 * time.Second) // даём время для инициализации
	fmt.Println("Waiting for self info...")

	// Ждём получения SelfInfo (или таймаут)
	select {
	case <-selfInfoReceived:
		fmt.Println("Self info ready, requesting channels...")
	case <-time.After(5 * time.Second):
		fmt.Println("Timeout waiting for self info")
	}

	// Запрашиваем каналы
	if err := client.RequestChannels(8); err != nil {
		panic(err)
	}

	// Ждём ответы на каналы
	time.Sleep(5 * time.Second)
	fmt.Println("Done")

	// Ждём немного, чтобы каналы успели прийти
	time.Sleep(2 * time.Second)

	// Запрос всех контактов (since=0)
	if err := client.RequestContacts(0); err != nil {
		fmt.Printf("RequestContacts error: %v\n", err)
	}

	// Ждём, пока контакты загрузятся (не надёжно, но для теста)
	time.Sleep(3 * time.Second)

	contacts := client.GetContacts()

	//client.SendMessage(&cont, "ответь ок")
	for _, v := range contacts {
		if v.Name == "Короед" {
			if err := client.SendMessage(v, "cli ping"); err != nil {
				fmt.Printf("Failed to send message: %v\n", err)
			} else {
				fmt.Println("Message sent")
			}
		}
		fmt.Printf("%v+", v)
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
		return bluetooth.Address{}, "", fmt.Errorf("timeout")
	}
}
