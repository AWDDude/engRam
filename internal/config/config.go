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
//
// A config file left over from an earlier version may still carry
// "default_min_score"; it is ignored rather than rejected, since search no
// longer has a similarity threshold to set.
type Config struct {
	Model        ModelConfig `json:"model"`
	DB           DBConfig    `json:"db"`
	DefaultLimit int         `json:"default_limit"`
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
		DefaultLimit: 20,
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

// configFilePath resolves the config file location: ENGRAM_CONFIG_PATH if
// set, otherwise config.json in the platform config directory, falling back
// to ~/.engram if that cannot be determined.
func configFilePath() string {
	if p := os.Getenv("ENGRAM_CONFIG_PATH"); p != "" {
		return p
	}
	cfgDir, err := configDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		cfgDir = filepath.Join(home, ".engram")
	}
	return filepath.Join(cfgDir, "config.json")
}

// Load reads config from ENGRAM_CONFIG_PATH, falling back to config.json in
// the platform config directory. If the file does not exist it is created with
// default values. ENGRAM_CONFIG_PATH skips auto-creation.
//
// Fields absent from an existing config file are filled in with their
// defaults and reported on stderr, rather than being rejected. The file on
// disk is left untouched: it may be managed by a dotfiles tool, and silently
// rewriting it would show up there as drift.
func Load() (Config, error) {
	path := configFilePath()
	cfg, defaulted, err := load(path)
	if len(defaulted) > 0 {
		fmt.Fprintf(os.Stderr, "engram: config %s: using defaults for %s\n",
			path, strings.Join(defaulted, ", "))
	}
	return cfg, err
}

// load does the work behind Load, returning the names of any fields that fell
// back to their default so the caller can report them. Split out so the
// defaulting logic is testable without capturing stderr.
func load(configPath string) (Config, []string, error) {
	cfg := Default()

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		if err := writeDefault(configPath, cfg); err != nil {
			return cfg, nil, fmt.Errorf("creating default config %s: %w", configPath, err)
		}
		return cfg, nil, nil
	}
	if err != nil {
		return cfg, nil, fmt.Errorf("reading config %s: %w", configPath, err)
	}

	var parsed Config
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Config{}, nil, fmt.Errorf("parsing config %s: %w", configPath, err)
	}

	// default_limit's zero value is also its documented "unlimited" meaning
	// (see store.Search), so an explicit 0 must be distinguished from an
	// omitted field via a separate presence check rather than parsed.DefaultLimit == 0.
	var presence struct {
		DefaultLimit *int `json:"default_limit"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return Config{}, nil, fmt.Errorf("parsing config %s: %w", configPath, err)
	}

	// Absent fields take their default. Only values the file actually sets are
	// validated below — a field the user never wrote can't be wrong.
	var defaulted []string
	if parsed.Model.Path == "" {
		parsed.Model.Path = cfg.Model.Path
		defaulted = append(defaulted, "model.path")
	}
	if parsed.Model.EmbeddingModel == "" {
		parsed.Model.EmbeddingModel = cfg.Model.EmbeddingModel
		defaulted = append(defaulted, "model.embedding_model")
	}
	if parsed.DB.Path == "" {
		parsed.DB.Path = cfg.DB.Path
		defaulted = append(defaulted, "db.path")
	}
	if presence.DefaultLimit == nil {
		parsed.DefaultLimit = cfg.DefaultLimit
		defaulted = append(defaulted, "default_limit")
	} else if parsed.DefaultLimit < 0 {
		return Config{}, nil, fmt.Errorf("config %s: default_limit must not be negative", configPath)
	}
	return parsed, defaulted, nil
}
