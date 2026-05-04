package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all runtime configuration for engram.
type Config struct {
	ModelDir string `json:"model_dir"`
	DBPath   string `json:"db_path"`
}

// Default returns configuration defaults derived from the running binary's
// location so the install directory is self-contained.
func Default() Config {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	return Config{
		ModelDir: filepath.Join(dir, "models"),
		DBPath:   filepath.Join(dir, "db"),
	}
}

// Load reads config from ENGRAM_CONFIG_PATH, falling back to config.json
// beside the binary. A missing file is not an error — defaults apply.
func Load() (Config, error) {
	cfg := Default()

	configPath := os.Getenv("ENGRAM_CONFIG_PATH")
	if configPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return cfg, fmt.Errorf("resolving executable path: %w", err)
		}
		configPath = filepath.Join(filepath.Dir(exe), "config.json")
	}

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("reading config %s: %w", configPath, err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config %s: %w", configPath, err)
	}
	return cfg, nil
}
