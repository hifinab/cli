package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// computeConfig holds choices saved by hi compute commands.
type computeConfig struct {
	HFNamespace string `json:"hf_namespace,omitempty"`
}

func computeConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "hi", "compute.json")
}

func loadComputeConfig() (computeConfig, error) {
	var config computeConfig
	data, err := os.ReadFile(computeConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	return config, json.Unmarshal(data, &config)
}

func saveHFNamespace(namespace string) error {
	config, err := loadComputeConfig()
	if err != nil {
		return err
	}
	config.HFNamespace = namespace
	path := computeConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
