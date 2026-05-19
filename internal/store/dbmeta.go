package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)


type dbMeta struct {
	ActiveModel string `json:"active_model"`
}

// ModelChangedError is returned by NewChromemStore when the configured model
// differs from the one used to build the existing database.
type ModelChangedError struct {
	OldModel string
	NewModel string
}

func (e *ModelChangedError) Error() string {
	return fmt.Sprintf(
		"embedding model changed from %q to %q — run 'engram migrate' to re-embed all memories",
		e.OldModel, e.NewModel,
	)
}

func dbMetaPath(dbPath string) string {
	return filepath.Join(dbPath, "db_meta.json")
}

func loadDBMeta(dbPath string) (dbMeta, error) {
	var m dbMeta
	data, err := os.ReadFile(dbMetaPath(dbPath))
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("reading db meta: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parsing db meta: %w", err)
	}
	return m, nil
}

func saveDBMeta(dbPath string, m dbMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dbMetaPath(dbPath), data, 0600)
}

// modelCollectionPath returns the namespaced subdirectory for a given model
// within dbPath, sanitizing the model name for use as a directory name.
func modelCollectionPath(dbPath, model string) string {
	safe := strings.ReplaceAll(model, "/", "_")
	return filepath.Join(dbPath, safe)
}

