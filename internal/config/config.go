package config

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	once   sync.Once
	cfg    *Config
	cfgErr error
)

// Load инициализирует конфигурацию из файла, если она ещё не загружена.
// Вызывается один раз (thread-safe).
func Load(path string) error {
	once.Do(func() {
		data, err := os.ReadFile(path)
		if err != nil {
			cfgErr = fmt.Errorf("reading config file: %w", err)
			return
		}
		var c Config
		if err := yaml.Unmarshal(data, &c); err != nil {
			cfgErr = fmt.Errorf("unmarshaling config: %w", err)
			return
		}
		cfg = &c
	})
	return cfgErr
}

// Get возвращает загруженную конфигурацию. Если Load ещё не вызывался или завершился ошибкой, возвращает nil.
func Get() *Config {
	return cfg
}
