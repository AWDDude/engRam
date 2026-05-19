package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Config holds all runtime configuration for engram.
type Config struct {
	ModelDir string `json:"model_dir"`
	DBPath   string `json:"db_path"`
}

// dataDir returns the platform-appropriate user data directory for engram.
// XDG_DATA_HOME is honored on any platform if set.
// Linux fallback: ~/.local/share/engram
// macOS fallback: ~/Library/Application Support/engram
// Windows fallback: %APPDATA%\engram
func dataDir() (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "engram"), nil
	}
	if runtime.GOOS == "linux" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", "engram"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "engram"), nil
}

// Default returns configuration with platform-appropriate data directory
// defaults. Falls back to ~/.engram if the data directory cannot be resolved.
func Default() Config {
	base, err := dataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".engram")
	}
	return Config{
		ModelDir: filepath.Join(base, "models"),
		DBPath:   filepath.Join(base, "db"),
	}
}

// configDir returns the platform-appropriate user config directory for engram.
// XDG_CONFIG_HOME is honored on any platform if set.
// Linux fallback: ~/.config/engram
// macOS fallback: ~/Library/Application Support/engram
// Windows fallback: %APPDATA%\engram
func configDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "engram"), nil
	}
	if runtime.GOOS == "linux" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "engram"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "engram"), nil
}

// Load reads config from ENGRAM_CONFIG_PATH, falling back to config.json in
// the platform config directory. A missing file is not an error — defaults apply.
func Load() (Config, error) {
	cfg := Default()

	configPath := os.Getenv("ENGRAM_CONFIG_PATH")
	if configPath == "" {
		cfgDir, err := configDir()
		if err != nil {
			home, _ := os.UserHomeDir()
			cfgDir = filepath.Join(home, ".engram")
		}
		configPath = filepath.Join(cfgDir, "config.json")
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
