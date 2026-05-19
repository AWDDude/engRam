package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ModelConfig holds model-related configuration.
type ModelConfig struct {
	Path string `json:"path"`
	// WARNING: changing EmbeddingModel invalidates the existing vector database.
	// All stored memories must be deleted and re-added after switching models.
	EmbeddingModel string `json:"embedding_model"`
}

// DBConfig holds database-related configuration.
type DBConfig struct {
	Path string `json:"path"`
}

// Config holds all runtime configuration for engram.
type Config struct {
	Model ModelConfig `json:"model"`
	DB    DBConfig    `json:"db"`
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
		Model: ModelConfig{
			Path:           filepath.Join(base, "models"),
			EmbeddingModel: "KnightsAnalytics/all-MiniLM-L6-v2",
		},
		DB: DBConfig{
			Path: filepath.Join(base, "db"),
		},
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

	// Split into raw sections first, then unmarshal each into the already-defaulted
	// sub-struct so that omitted fields within a section keep their defaults.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return cfg, fmt.Errorf("parsing config %s: %w", configPath, err)
	}
	if v, ok := raw["model"]; ok {
		if err := json.Unmarshal(v, &cfg.Model); err != nil {
			return cfg, fmt.Errorf("parsing config model section: %w", err)
		}
	}
	if v, ok := raw["db"]; ok {
		if err := json.Unmarshal(v, &cfg.DB); err != nil {
			return cfg, fmt.Errorf("parsing config db section: %w", err)
		}
	}
	return cfg, nil
}
