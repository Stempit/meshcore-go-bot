package config

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	once        sync.Once
	cfg         *Config
	cfgErr      error
	rawSections map[string]yaml.Node // raw top-level YAML nodes, for per-plugin decoding
)

// Sectioner gives a plugin access to its own raw YAML section from the single
// config file, decoded on demand into the plugin's own struct (two-pass YAML).
type Sectioner interface {
	// Section decodes the top-level section named key into target. A missing
	// section is not an error: the caller's defaults stand (returns nil).
	Section(key string, target any) error
}

type sectioner struct{}

func (sectioner) Section(key string, target any) error {
	n, ok := rawSections[key]
	if !ok {
		return nil // absent section → caller's defaults stand
	}
	return n.Decode(target)
}

// Sections returns a Sectioner over the loaded config. Call after Load.
func Sections() Sectioner { return sectioner{} }

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
		// Second pass over the same document: keep every top-level section as a
		// raw node so plugins can decode their own section via Sectioner.
		var raw map[string]yaml.Node
		if err := yaml.Unmarshal(data, &raw); err != nil {
			cfgErr = fmt.Errorf("unmarshaling config sections: %w", err)
			return
		}
		rawSections = raw
		cfg = &c
	})
	return cfgErr
}

// Get returns the loaded configuration.
// Returns nil if Load has not been called or returned an error.
func Get() *Config {
	return cfg
}
