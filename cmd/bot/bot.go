package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// Замените на реальные UUID из документации MeshCore
const (
	meshCoreServiceUUID = "6e400001-b5a3-f393-e0a9-e50e24dcca9e"
	lastMessageCharUUID = "6e400003-b5a3-f393-e0a9-e50e24dcca9e"
)

func main() {
	var (
		mac      string
		scan     bool
		charUUID string
	)
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch arg {
		case "--mac":
			if i+1 < len(os.Args) {
				mac = os.Args[i+1]
				i++
			}
		case "--scan":
			scan = true
		case "--char-uuid":
			if i+1 < len(os.Args) {
				charUUID = os.Args[i+1]
				i++
			}
		}
	}
	if charUUID == "" {
		charUUID = lastMessageCharUUID
	}

	if mac != "" {
		if err := connectAndRead(mac, charUUID); err != nil {
			fmt.Printf("Ошибка: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if scan {
		if err := scanAndSelect(charUUID); err != nil {
			fmt.Printf("Ошибка: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Println("Использование:")
	fmt.Println("  meshcore-bot --mac AA:BB:CC:DD:EE:FF [--char-uuid UUID]")
	fmt.Println("  meshcore-bot --scan [--char-uuid UUID]")
	fmt.Println("  --char-uuid необязателен, по умолчанию используется предустановленный UUID для MeshCore.")
}

func scanAndSelect(charUUID string) error {
	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("не удалось включить адаптер: %w", err)
	}
	fmt.Println("Bluetooth адаптер включён, начинаем сканирование...")

	var devices []bluetooth.ScanResult
	var mu sync.Mutex
	seen := make(map[string]bool)

	err := adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		mu.Lock()
		defer mu.Unlock()
		addr := result.Address.String()
		if !seen[addr] {
			seen[addr] = true
			devices = append(devices, result)
			name := result.LocalName()
			if name == "" {
				name = "<без имени>"
			}
			fmt.Printf("Найдено: %s (%s)\n", name, addr)
		}
	})
	if err != nil {
		return fmt.Errorf("ошибка сканирования: %w", err)
	}

	fmt.Println("Сканирование будет длиться 10 секунд...")
	time.Sleep(10 * time.Second)
	adapter.StopScan()

	if len(devices) == 0 {
		return fmt.Errorf("устройства не найдены")
	}

	fmt.Println("Доступные устройства:")
	for i, d := range devices {
		name := d.LocalName()
		if name == "" {
			name = "<без имени>"
		}
		fmt.Printf("[%d] %s (%s)\n", i, name, d.Address.String())
	}

	fmt.Print("Выберите номер устройства: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	var idx int
	if _, err := fmt.Sscanf(input, "%d", &idx); err != nil || idx < 0 || idx >= len(devices) {
		return fmt.Errorf("неверный выбор")
	}

	mac := devices[idx].Address.String()
	fmt.Printf("Подключение к %s...\n", mac)
	return connectAndRead(mac, charUUID)
}

func connectAndRead(macAddr string, charUUID string) error {
	adapter := bluetooth.DefaultAdapter
	// Адаптер скорее всего уже включён, но на всякий случай
	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("не удалось включить адаптер: %w", err)
	}
	fmt.Println("[1] Адаптер включён")

	var device *bluetooth.Device
	done := make(chan error)

	fmt.Println("[2] Начинаем сканирование для поиска устройства", macAddr)
	err := adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		if result.Address.String() == macAddr {
			fmt.Println("[3] Устройство найдено, подключаемся...")
			dev, err := adapter.Connect(result.Address, bluetooth.ConnectionParams{})
			if err != nil {
				done <- fmt.Errorf("ошибка подключения: %w", err)
				return
			}
			device = &dev
			adapter.StopScan()
			done <- nil
		}
	})
	if err != nil {
		return fmt.Errorf("ошибка сканирования: %w", err)
	}

	select {
	case err := <-done:
		if err != nil {
			return err
		}
		fmt.Println("[4] Подключение установлено")
	case <-time.After(10 * time.Second):
		adapter.StopScan()
		return fmt.Errorf("таймаут: устройство %s не найдено", macAddr)
	}

	if device == nil {
		return fmt.Errorf("не удалось получить устройство")
	}
	defer device.Disconnect()

	fmt.Println("[5] Ищем сервисы...")
	services, err := device.DiscoverServices(nil)
	if err != nil {
		return fmt.Errorf("ошибка поиска сервисов: %w", err)
	}
	fmt.Printf("[6] Найдено сервисов: %d\n", len(services))

	var characteristic *bluetooth.Characteristic
	for _, service := range services {
		chars, err := service.DiscoverCharacteristics(nil)
		if err != nil {
			continue
		}
		fmt.Printf("Сервис %s содержит %d характеристик:\n", service.UUID().String(), len(chars))
		for _, char := range chars {
			fmt.Printf("  Характеристика: %s\n", char.UUID().String())
		}

		if characteristic != nil {
			break
		}
	}

	if characteristic == nil {
		return fmt.Errorf("характеристика с UUID %s не найдена", charUUID)
	}
	fmt.Println("[7] Характеристика найдена, читаем...")

	buf := make([]byte, 512)
	n, err := characteristic.Read(buf)
	if err != nil {
		return fmt.Errorf("ошибка чтения характеристики: %w", err)
	}
	data := buf[:n]

	fmt.Printf("Последнее сообщение (hex): %s\n", hex.EncodeToString(data))
	fmt.Printf("Как текст: %s\n", string(data))

	return nil
}
