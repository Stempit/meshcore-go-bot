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
	Enabled bool            `yaml:"enabled"`
	SeenDir string          `yaml:"seen_dir"` // defaults to "log/news"
	Feeds   []RSSFeedConfig `yaml:"feeds"`
}

// ShrinkLLMConfig configures the "llm" shrink variant. Type/Model are optional
// overrides that inherit from the top-level LLM section when empty, so a light
// model can shrink while a big model answers.
type ShrinkLLMConfig struct {
	Type        string  `yaml:"type"`
	Model       string  `yaml:"model"`
	Prompt      string  `yaml:"prompt"`
	Temperature float64 `yaml:"temperature"`
	MaxRetries  int     `yaml:"max_retries"`
}

// ShrinkConfig is the unified mesh-fit configuration. Default is the global
// variant; Overrides selects a variant per command/source name.
type ShrinkConfig struct {
	Default   string            `yaml:"default"`   // truncate|split|llm|skip
	MaxBytes  int               `yaml:"max_bytes"` // hard mesh limit, default 140
	Ellipsis  string            `yaml:"ellipsis"`  // appended by truncate
	Overrides map[string]string `yaml:"overrides"` // name -> variant
	LLM       ShrinkLLMConfig   `yaml:"llm"`
}

type ConnectionConfig struct {
	// Type selects the transport: "bt" (Bluetooth LE, default) or "serial" (USB/UART).
	Type string `yaml:"type"`
	// SerialPort is the OS device path used when Type is "serial"
	// (e.g. "/dev/ttyUSB0", "/dev/tty.usbmodem14101", "COM3").
	SerialPort string `yaml:"serial_port"`
	// SerialBaud sets the baud rate for the serial transport (default 115200).
	SerialBaud int `yaml:"serial_baud"`
	// ChannelTransmissions is the MAXIMUM number of times each channel reply is
	// transmitted (the same packet, deduplicated by receivers) to improve
	// delivery against silent RF drops. The bot stops early once it hears the
	// mesh rebroadcast the message, so fewer transmissions are sent on a healthy
	// network. This is our own node sending the packet, not the network "repeat"
	// by repeater nodes. 0 or unset defaults to 3; 1 sends a single transmission.
	ChannelTransmissions int `yaml:"channel_transmissions"`
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

type StatsConfig struct {
	URL         string `yaml:"url"`           // HTTP(S) URL or file:///path/to/stats.json
	CacheTTLSec int    `yaml:"cache_ttl_sec"` // default 300
}

type Config struct {
	Connection ConnectionConfig        `yaml:"Connection"`
	Proxy      ProxyConfig             `yaml:"Proxy"`
	MQTT       mqttbridge.MQTTConfig   `yaml:"MQTT"`
	Telegram   tgbridge.TelegramConfig `yaml:"Telegram"`
	Trace      TraceConfig             `yaml:"Trace"`
	Security   SecurityConfig          `yaml:"Security"`
	RSS        RSSConfig               `yaml:"RSS"`
	Weather    WeatherConfig           `yaml:"Weather"`
	Stats      StatsConfig             `yaml:"Stats"`
	Shrink     ShrinkConfig            `yaml:"Shrink"`
	LLM        struct {
		Enabled           bool    `yaml:"enabled"`
		Type              string  `yaml:"type"`
		Model             string  `yaml:"model"`
		SystemPrompt      string  `yaml:"system_prompt"`
		SystemTemperature float64 `yaml:"system_temperature"`
	} `yaml:"LLM"`
	Claw struct {
		Enabled    bool   `yaml:"enabled"`
		URL        string `yaml:"url"`         // ws://host:18790/pico/ws (gateway port, not dashboard)
		Token      string `yaml:"token"`       // from picoclaw dashboard → Pico token
		TimeoutSec int    `yaml:"timeout_sec"` // default 180
		BotName    string `yaml:"bot_name"`    // mesh display name of the bot, e.g. "Yorick 💀"
	} `yaml:"Claw"`
}
