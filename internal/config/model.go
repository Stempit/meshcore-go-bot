package config

import (
	"meshbot/internal/mqttbridge"
	"meshbot/internal/tgbridge"
)

type Config struct {
	MQTT      mqttbridge.MQTTConfig  `yaml:"MQTT"`
	Telegram  tgbridge.TelegramConfig `yaml:"Telegram"`
	LLM struct {
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
