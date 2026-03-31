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

type Config struct {
	MQTT     mqttbridge.MQTTConfig   `yaml:"MQTT"`
	Telegram tgbridge.TelegramConfig `yaml:"Telegram"`
	Trace    TraceConfig             `yaml:"Trace"`
	LLM      struct {
		Enabled           bool    `yaml:"enabled"`
		Type              string  `yaml:"type"`
		Model             string  `yaml:"model"`
		SystemPrompt      string  `yaml:"system_prompt"`
		SystemTemperature float64 `yaml:"system_temperature"`
		ShrinkPrompt      string  `yaml:"shrink_prompt"`
		ShrinkTemperature float64 `yaml:"shrink_temperature"`
		ShrinkMaxRetries  int     `yaml:"shrink_max_reties"`
	} `yaml:"LLM"`
	// Database struct {
	// 	Name            string `yaml:"name"`
	// 	ConnMaxLifetime int    `yaml:"conn_max_lifetime"`
	// } `yaml:"database"`
}
