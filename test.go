package main

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"tinygo.org/x/bluetooth"
)

// UUID сервиса и характеристик
const (
	serviceUUID          = "6e400001-b5a3-f393-e0a9-e50e24dcca9e"
	rxCharacteristicUUID = "6e400002-b5a3-f393-e0a9-e50e24dcca9e"
	txCharacteristicUUID = "6e400003-b5a3-f393-e0a9-e50e24dcca9e"
)

// Коды команд и ответов (из протокола)
const (
	cmdAppStart    = 0x01
	cmdDeviceQuery = 0x16
	cmdGetChannel  = 0x1F

	respCodeChannelInfo = 0x12
)

var adapter = bluetooth.DefaultAdapter

func main() {
	// Включаем адаптер
	if err := adapter.Enable(); err != nil {
		panic(fmt.Errorf("failed to enable adapter: %w", err))
	}
	fmt.Println("Bluetooth adapter enabled")

	// Сканируем устройства с именем, начинающимся на "MeshCore-"
	fmt.Println("Scanning for MeshCore devices...")
	device, err := scanForMeshCoreDevice()
	if err != nil {
		panic(fmt.Errorf("scan failed: %w", err))
	}
	fmt.Printf("Found device: %s (%s)\n", device.LocalName(), device.Address.String())

	// Подключаемся
	fmt.Println("Connecting...")
	client, err := adapter.Connect(device.Address, bluetooth.ConnectionParams{})
	if err != nil {
		panic(fmt.Errorf("connection failed: %w", err))
	}
	defer client.Disconnect()
	fmt.Println("Connected")

	// Ищем сервис
	services, err := client.DiscoverServices(nil)
	if err != nil {
		panic(fmt.Errorf("service discovery failed: %w", err))
	}
	var targetService *bluetooth.DeviceService
	for _, svc := range services {
		if svc.UUID().String() == serviceUUID {
			targetService = &svc
			break
		}
	}
	if targetService == nil {
		panic("MeshCore service not found")
	}
	fmt.Println("Found MeshCore service")

	// Находим характеристики
	chars, err := targetService.DiscoverCharacteristics(nil)
	if err != nil {
		panic(fmt.Errorf("characteristic discovery failed: %w", err))
	}
	var rxChar, txChar *bluetooth.DeviceCharacteristic
	for _, ch := range chars {
		uuid := ch.UUID().String()
		switch uuid {
		case rxCharacteristicUUID:
			rxChar = &ch
		case txCharacteristicUUID:
			txChar = &ch
		}
	}
	if rxChar == nil || txChar == nil {
		panic("RX or TX characteristic not found")
	}
	fmt.Println("Found RX/TX characteristics")

	// Подписываемся на уведомления от TX характеристики
	notifyCh := make(chan []byte, 10)
	if err := txChar.EnableNotifications(func(data []byte) {
		notifyCh <- data
	}); err != nil {
		panic(fmt.Errorf("failed to enable notifications: %w", err))
	}
	fmt.Println("Notifications enabled")

	// Отправляем инициализационные команды
	fmt.Println("Sending init commands...")
	if err := sendAppStart(rxChar); err != nil {
		panic(fmt.Errorf("sendAppStart failed: %w", err))
	}
	if err := sendDeviceQuery(rxChar); err != nil {
		panic(fmt.Errorf("sendDeviceQuery failed: %w", err))
	}

	// Ждём немного, чтобы устройство обработало
	time.Sleep(500 * time.Millisecond)

	// Запрашиваем каналы (обычно от 0 до 7)
	maxChannels := 8
	fmt.Println("Requesting channels...")
	for i := 0; i < maxChannels; i++ {
		if err := sendGetChannel(rxChar, byte(i)); err != nil {
			fmt.Printf("Warning: failed to send channel %d request: %v\n", i, err)
		}
	}

	// Собираем ответы
	fmt.Println("\nWaiting for channel info responses...")
	channels := make([]ChannelInfo, 0)
	timeout := time.After(5 * time.Second)

loop:
	for {
		select {
		case data := <-notifyCh:
			if len(data) == 0 {
				continue
			}
			// Обрабатываем ответ
			if data[0] == respCodeChannelInfo {
				ch, err := parseChannelInfo(data)
				if err != nil {
					fmt.Printf("Failed to parse channel info: %v\n", err)
				} else {
					channels = append(channels, ch)
					fmt.Printf("Channel %d: %s\n", ch.Index, ch.Name)
				}
			} else {
				// Игнорируем другие ответы
				fmt.Printf("Received other response: 0x%02x\n", data[0])
			}
		case <-timeout:
			break loop
		}
	}

	if len(channels) == 0 {
		fmt.Println("No channel info received")
	} else {
		fmt.Println("\n=== List of channels ===")
		for _, ch := range channels {
			fmt.Printf("Index: %d, Name: %s\n", ch.Index, ch.Name)
		}
	}
}

// Структура для хранения информации о канале
type ChannelInfo struct {
	Index int
	Name  string
}

// Функция сканирования для поиска устройства MeshCore
func scanForMeshCoreDevice() (*bluetooth.ScanResult, error) {
	resultChan := make(chan bluetooth.ScanResult)
	//errChan := make(chan error)

	// Запускаем сканирование
	err := adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
		if strings.HasPrefix(result.LocalName(), "MeshCore-") {
			adapter.StopScan()
			resultChan <- result
		}
	})
	if err != nil {
		return nil, err
	}

	// Ждём результат с таймаутом
	select {
	case res := <-resultChan:
		return &res, nil
	case <-time.After(10 * time.Second):
		adapter.StopScan()
		return nil, fmt.Errorf("timeout: no MeshCore device found")
	}
}

// Отправка команды APP_START
func sendAppStart(rxChar *bluetooth.DeviceCharacteristic) error {
	// Формат: [cmd][app_ver][reserved 6][app_name]\0
	appName := "MeshCoreGo"
	ver := byte(1)
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdAppStart)
	buf.WriteByte(ver)
	buf.Write([]byte{0, 0, 0, 0, 0, 0}) // 6 байт reserved
	buf.WriteString(appName)
	buf.WriteByte(0) // null terminator
	_, err := rxChar.Write(buf.Bytes())
	return err
}

// Отправка команды DEVICE_QUERY
func sendDeviceQuery(rxChar *bluetooth.DeviceCharacteristic) error {
	// Формат: [cmd][app_ver]
	appVer := byte(3) // appProtocolVersion из Dart кода
	_, err := rxChar.Write([]byte{cmdDeviceQuery, appVer})
	return err
}

// Отправка команды GET_CHANNEL
func sendGetChannel(rxChar *bluetooth.DeviceCharacteristic, index byte) error {
	_, err := rxChar.Write([]byte{cmdGetChannel, index})
	return err
}

// Парсинг ответа RESP_CODE_CHANNEL_INFO
func parseChannelInfo(data []byte) (ChannelInfo, error) {
	// Ожидаемая длина: минимум 2 байта (код + индекс), имя до 32 байт
	if len(data) < 2 {
		return ChannelInfo{}, fmt.Errorf("data too short")
	}
	if data[0] != respCodeChannelInfo {
		return ChannelInfo{}, fmt.Errorf("not a channel info response")
	}
	index := int(data[1])

	// Имя канала: начинается с байта 2, максимум 32 байта, null-terminated
	nameBytes := data[2:]
	nullIdx := bytes.IndexByte(nameBytes, 0)
	if nullIdx == -1 {
		nullIdx = len(nameBytes)
	}
	name := string(nameBytes[:nullIdx])
	// Игнорируем пустые каналы (имя пустое или все нули)
	if len(name) == 0 {
		name = "(empty)"
	}
	return ChannelInfo{Index: index, Name: name}, nil
}
