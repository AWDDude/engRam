package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// dataDir returns the user data directory for engram.
// XDG_DATA_HOME is honored if set; otherwise defaults to ~/.local/share/engram on all platforms.
func dataDir() (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "engram"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "engram"), nil
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

// configDir returns the user config directory for engram.
// XDG_CONFIG_HOME is honored if set; otherwise defaults to ~/.config/engram on all platforms.
func configDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "engram"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "engram"), nil
}

// writeDefault marshals cfg as indented JSON and writes it to path,
// creating any parent directories as needed.
func writeDefault(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Load reads config from ENGRAM_CONFIG_PATH, falling back to config.json in
// the platform config directory. If the file does not exist it is created with
// default values. ENGRAM_CONFIG_PATH skips auto-creation.
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
		if err := writeDefault(configPath, cfg); err != nil {
			return cfg, fmt.Errorf("creating default config %s: %w", configPath, err)
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("reading config %s: %w", configPath, err)
	}

	var parsed Config
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", configPath, err)
	}

	var missing []string
	if parsed.Model.Path == "" {
		missing = append(missing, "model.path")
	}
	if parsed.Model.EmbeddingModel == "" {
		missing = append(missing, "model.embedding_model")
	}
	if parsed.DB.Path == "" {
		missing = append(missing, "db.path")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("config %s missing required fields: %s", configPath, strings.Join(missing, ", "))
	}
	return parsed, nil
}
