package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Model.Path == "" {
		t.Error("expected non-empty default Model.Path")
	}
	if cfg.Model.EmbeddingModel == "" {
		t.Error("expected non-empty default Model.EmbeddingModel")
	}
	if cfg.DB.Path == "" {
		t.Error("expected non-empty default DB.Path")
	}
	if cfg.DefaultLimit != 20 {
		t.Errorf("expected default_limit 20, got %v", cfg.DefaultLimit)
	}
}

func TestLoad_AutoCreate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ENGRAM_CONFIG_PATH", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.Path == "" {
		t.Error("expected non-empty default Model.Path")
	}

	createdPath := filepath.Join(dir, "engram", "config.json")
	data, err := os.ReadFile(createdPath)
	if err != nil {
		t.Fatalf("expected config file to be created at %s: %v", createdPath, err)
	}
	var written Config
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("created config is not valid JSON: %v", err)
	}
	if written.Model.EmbeddingModel != cfg.Model.EmbeddingModel {
		t.Errorf("written embedding model %q does not match default %q", written.Model.EmbeddingModel, cfg.Model.EmbeddingModel)
	}
}

func TestLoad_AutoCreate_ViaEnvPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom", "config.json")
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected config file to be created at %s: %v", path, err)
	}
	var written Config
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("created config is not valid JSON: %v", err)
	}
	if written.Model.EmbeddingModel != cfg.Model.EmbeddingModel {
		t.Errorf("written embedding model %q does not match default %q", written.Model.EmbeddingModel, cfg.Model.EmbeddingModel)
	}
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":         map[string]string{"path": "/custom/models", "embedding_model": "custom/model"},
		"db":            map[string]string{"path": "/custom/db"},
		"default_limit": 20,
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.Path != "/custom/models" {
		t.Errorf("expected custom model path, got %s", cfg.Model.Path)
	}
	if cfg.Model.EmbeddingModel != "custom/model" {
		t.Errorf("expected custom embedding model, got %s", cfg.Model.EmbeddingModel)
	}
	if cfg.DB.Path != "/custom/db" {
		t.Errorf("expected custom db path, got %s", cfg.DB.Path)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("not json{"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	_, err := Load()
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestDefault_XDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/xdg/data")
	cfg := Default()
	if !strings.HasPrefix(cfg.Model.Path, "/custom/xdg/data") {
		t.Errorf("XDG_DATA_HOME not honored for model path, got: %s", cfg.Model.Path)
	}
	if !strings.HasPrefix(cfg.DB.Path, "/custom/xdg/data") {
		t.Errorf("XDG_DATA_HOME not honored for db path, got: %s", cfg.DB.Path)
	}
}

func TestLoad_XDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ENGRAM_CONFIG_PATH", "")

	cfgDir := filepath.Join(dir, "engram")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{
		"model":         map[string]string{"path": "/xdg/config/models", "embedding_model": "custom/model"},
		"db":            map[string]string{"path": "/xdg/config/db"},
		"default_limit": 20,
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.Path != "/xdg/config/models" {
		t.Errorf("XDG_CONFIG_HOME not honored for config lookup, got model path: %s", cfg.Model.Path)
	}
}

func TestLoad_MissingFieldsTakeDefaults(t *testing.T) {
	// A partial config must load, not fail: absent fields fall back to their
	// default and the value the file *does* set is preserved.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model": map[string]string{"path": "/custom/models"},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, defaulted, err := load(path)
	if err != nil {
		t.Fatalf("expected a partial config to load, got error: %v", err)
	}
	if cfg.Model.Path != "/custom/models" {
		t.Errorf("expected the explicitly set model.path to survive, got %q", cfg.Model.Path)
	}

	def := Default()
	if cfg.Model.EmbeddingModel != def.Model.EmbeddingModel {
		t.Errorf("model.embedding_model = %q, want default %q", cfg.Model.EmbeddingModel, def.Model.EmbeddingModel)
	}
	if cfg.DB.Path != def.DB.Path {
		t.Errorf("db.path = %q, want default %q", cfg.DB.Path, def.DB.Path)
	}
	if cfg.DefaultLimit != def.DefaultLimit {
		t.Errorf("default_limit = %d, want default %d", cfg.DefaultLimit, def.DefaultLimit)
	}

	want := []string{"model.embedding_model", "db.path", "default_limit"}
	if !reflect.DeepEqual(defaulted, want) {
		t.Errorf("defaulted fields = %v, want %v", defaulted, want)
	}
}

func TestLoad_FullyPopulatedConfigReportsNoDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":         map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":            map[string]string{"path": "/db"},
		"default_limit": 5,
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, defaulted, err := load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defaulted) != 0 {
		t.Errorf("expected no defaulted fields, got %v", defaulted)
	}
	if cfg.DefaultLimit != 5 {
		t.Errorf("default_limit = %d, want 5", cfg.DefaultLimit)
	}
}

func TestLoad_ExplicitZeroLimitIsNotTreatedAsMissing(t *testing.T) {
	// 0 is a meaningful value (unlimited), so it must not be mistaken for an
	// omitted field and overwritten with the default.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":         map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":            map[string]string{"path": "/db"},
		"default_limit": 0,
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, defaulted, err := load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultLimit != 0 {
		t.Errorf("expected an explicit 0 to be preserved, got %d", cfg.DefaultLimit)
	}
	for _, f := range defaulted {
		if f == "default_limit" {
			t.Error("an explicit 0 must not be reported as a defaulted field")
		}
	}
}

func TestLoad_EmptyObjectTakesAllDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, defaulted, err := load(path)
	if err != nil {
		t.Fatalf("expected an empty config to load, got error: %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("expected an empty config to equal Default(), got %+v", cfg)
	}
	if len(defaulted) != 4 {
		t.Errorf("expected all 4 fields reported as defaulted, got %v", defaulted)
	}
}

func TestLoad_DefaultLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":         map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":            map[string]string{"path": "/db"},
		"default_limit": 50,
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultLimit != 50 {
		t.Errorf("expected default_limit 50, got %v", cfg.DefaultLimit)
	}
}

func TestLoad_DefaultLimit_ExplicitZeroMeansUnlimited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":         map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":            map[string]string{"path": "/db"},
		"default_limit": 0,
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultLimit != 0 {
		t.Errorf("expected default_limit 0, got %v", cfg.DefaultLimit)
	}
}

func TestLoad_DefaultLimit_MissingTakesDefaultViaPublicLoad(t *testing.T) {
	// Same defaulting as the load() tests above, but through the exported
	// Load() and ENGRAM_CONFIG_PATH, so the whole path a user hits is covered.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model": map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":    map[string]string{"path": "/db"},
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected a config without default_limit to load, got error: %v", err)
	}
	if cfg.DefaultLimit != Default().DefaultLimit {
		t.Errorf("default_limit = %d, want default %d", cfg.DefaultLimit, Default().DefaultLimit)
	}
	if cfg.DB.Path != "/db" {
		t.Errorf("expected explicitly set db.path to survive, got %q", cfg.DB.Path)
	}

	// The file must not be rewritten — it may be managed by a dotfiles tool.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, data) {
		t.Errorf("Load rewrote the config file:\n before: %s\n after:  %s", data, after)
	}
}

func TestLoad_DefaultLimit_Negative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{
		"model":         map[string]string{"path": "/m", "embedding_model": "custom/model"},
		"db":            map[string]string{"path": "/db"},
		"default_limit": -1,
	})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_CONFIG_PATH", path)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for negative default_limit, got nil")
	}
	if !strings.Contains(err.Error(), "default_limit") {
		t.Errorf("expected error to mention default_limit, got: %v", err)
	}
}
