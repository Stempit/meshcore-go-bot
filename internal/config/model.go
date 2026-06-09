package config

import (
	"meshbot/internal/mqttbridge"
	"meshbot/internal/tgbridge"
)

type TraceConfig struct {
	UseConfigCoords bool    `yaml:"use_config_coords"`
	Lat             float64 `yaml:"lat"`
	Lon             float64 `yaml:"lon"`
}

type SecurityListConfig struct {
	Whitelist []string `yaml:"whitelist"`
	Blacklist []string `yaml:"blacklist"`
}

type SecurityConfig struct {
	Channels SecurityListConfig `yaml:"channels"`
	Direct   SecurityListConfig `yaml:"direct"`
}

type RSSFeedConfig struct {
	Name     string `yaml:"name"` // used as filename in log/news/<name>.txt
	URL      string `yaml:"url"`
	Interval string `yaml:"interval"` // e.g. "5m", "1h"
}

type RSSConfig struct {
	Enabled   bool            `yaml:"enabled"`
	LongTitle string          `yaml:"long_title"` // "skip" or "shrink"
	SeenDir   string          `yaml:"seen_dir"`   // defaults to "log/news"
	Feeds     []RSSFeedConfig `yaml:"feeds"`
}

type ConnectionConfig struct {
	// Type selects the transport: "bt" (Bluetooth LE, default) or "serial" (USB/UART).
	Type string `yaml:"type"`
	// SerialPort is the OS device path used when Type is "serial"
	// (e.g. "/dev/ttyUSB0", "/dev/tty.usbmodem14101", "COM3").
	SerialPort string `yaml:"serial_port"`
	// SerialBaud sets the baud rate for the serial transport (default 115200).
	SerialBaud int `yaml:"serial_baud"`
}

type ProxyConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"` // e.g. "socks5://user:pass@host:1080"
}

type WeatherConfig struct {
	Enabled         bool   `yaml:"enabled"`
	DefaultLocation string `yaml:"default_location"`
	Units           string `yaml:"units"` // "metric" or "imperial"
}

type Config struct {
	Connection ConnectionConfig        `yaml:"Connection"`
	Proxy      ProxyConfig             `yaml:"Proxy"`
	MQTT       mqttbridge.MQTTConfig   `yaml:"MQTT"`
	Telegram tgbridge.TelegramConfig `yaml:"Telegram"`
	Trace    TraceConfig             `yaml:"Trace"`
	Security SecurityConfig          `yaml:"Security"`
	RSS      RSSConfig               `yaml:"RSS"`
	Weather  WeatherConfig           `yaml:"Weather"`
	LLM      struct {
		Enabled           bool    `yaml:"enabled"`
		Type              string  `yaml:"type"`
		Model             string  `yaml:"model"`
		SystemPrompt      string  `yaml:"system_prompt"`
		SystemTemperature float64 `yaml:"system_temperature"`
		ShrinkPrompt      string  `yaml:"shrink_prompt"`
		ShrinkTemperature float64 `yaml:"shrink_temperature"`
		ShrinkMaxRetries  int     `yaml:"shrink_max_retries"`
	} `yaml:"LLM"`
	Claw struct {
		Enabled    bool   `yaml:"enabled"`
		URL        string `yaml:"url"`         // ws://host:18790/pico/ws (gateway port, not dashboard)
		Token      string `yaml:"token"`       // from picoclaw dashboard → Pico token
		TimeoutSec int    `yaml:"timeout_sec"` // default 180
		BotName    string `yaml:"bot_name"`    // mesh display name of the bot, e.g. "Yorick 💀"
		Split      bool   `yaml:"split"`       // split long responses into multiple mesh messages instead of LLM shrink
	} `yaml:"Claw"`
}
