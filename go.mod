module github.com/stempit/meshcore-go-bot

go 1.27.1

require (
	filippo.io/edwards25519 v1.2.0
	github.com/OvyFlash/telegram-bot-api v0.0.0-20260826195316-83c4eada97a1
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/gorilla/websocket v1.5.3
	github.com/ollama/ollama v0.40.11
	github.com/revrost/go-openrouter v1.8.0
	github.com/stempit/meshcore-go v0.0.0-00010101000000-000000000000
	github.com/stempit/meshcore-kit v0.0.0-00010101000000-000000000000
	golang.org/x/net v0.61.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/saltosystems/winrt-go v0.0.0-20260317170058-9c2fec580d96 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/soypat/cyw43439 v0.1.2-0.20260731160358-f2a6af121857 // indirect
	github.com/soypat/lneto v0.3.2 // indirect
	github.com/soypat/seqs v0.0.0-20260125140838-2c1c6b1bd69e // indirect
	github.com/tinygo-org/cbgo v0.0.4 // indirect
	github.com/tinygo-org/pio v0.3.0 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	go.bug.st/serial v1.8.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/exp v0.0.0-20260727155853-b88d891fe743 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	tinygo.org/x/bluetooth v0.16.0 // indirect
	tinygo.org/x/espradio v0.3.0 // indirect
)

replace github.com/stempit/meshcore-go => ../meshcore-go

replace github.com/stempit/meshcore-kit => ../meshcore-kit
