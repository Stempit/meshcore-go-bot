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

// Load initializes the configuration from the given file path.
// It is safe to call from multiple goroutines; the file is read exactly once.
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

// Get returns the loaded configuration.
// Returns nil if Load has not been called or returned an error.
func Get() *Config {
	return cfg
}
