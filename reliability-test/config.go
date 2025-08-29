package main

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	BaseURL      string         `yaml:"baseUrl"`
	TestInterval int            `yaml:"testInterval"` // seconds between tests
	Timeouts     map[string]int `yaml:"timeouts"`     // timeouts per step in seconds
	AbortSteps   []string       `yaml:"abortSteps"`   // steps that cause shutdown on failure/timeout
}

func LoadConfig(filename string) (*Config, error) {
	// Default configuration
	config := &Config{
		BaseURL:      "http://boss.local:8080",
		TestInterval: 5,
		Timeouts: map[string]int{
			"init":                30,
			"secret gen":          150,
			"disk wipe":           235,
			"powerup":             60,
			"booting-hypervisors": 245,
			"booting-nodes":       235,
			"bootstrapping":       155,
			"finalizing":          255,
			"starting":            156,
		},
	}

	// If no config file specified, return defaults
	if filename == "" {
		return config, nil
	}

	// Try to load from file
	data, err := os.ReadFile(filename)
	if err != nil {
		return config, err // Return defaults even on error
	}

	err = yaml.Unmarshal(data, config)
	if err != nil {
		return config, err
	}

	return config, nil
}
