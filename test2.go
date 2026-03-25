package main

import (
	"fmt"
	"time"

	"tinygo.org/x/bluetooth"
)

var adapter = bluetooth.DefaultAdapter

func main() {
	// Включение адаптера
	must(adapter.Enable())

	// Сканирование устройств
	println("scanning...")
	err := adapter.Scan(func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) {
		if device.LocalName() == "MeshCore-7C44431A" { // замените на нужное имя
			println("found device:", device.Address.String(), device.LocalName())
			adapter.StopScan()
			time.Sleep(500 * time.Millisecond)
			go connect(device) // запускаем в горутине
			return
		}
	})
	must(err)

	select {}
}

func connect(device bluetooth.ScanResult) {
	println("attempting to connect to", device.Address.String())
	// Подключение
	client, err := adapter.Connect(device.Address, bluetooth.ConnectionParams{})
	if err != nil {
		println("connection error:", err.Error())
		return
	}
	defer client.Disconnect()
	println("connected!")

	// Поиск сервисов
	services, err := client.DiscoverServices(nil)
	if err != nil {
		println("discover services error:", err.Error())
		return
	}
	println("services found:", len(services))
	for _, svc := range services {
		fmt.Printf("  Service: %s\n", svc.UUID().String())
	}

	for _, svc := range services {
		fmt.Printf("Service: %s\n", svc.UUID().String())
		chars, _ := svc.DiscoverCharacteristics(nil)
		for _, ch := range chars {
			fmt.Printf("  Char: %s\n", ch.UUID().String())
		}
	}

	// Пример чтения характеристики
	// char, _ := service.FindCharacteristic(bluetooth.NewUUID([16]byte{...}))
	// data, _ := char.Read(nil)
	// fmt.Printf("Data: %x\n", data)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
