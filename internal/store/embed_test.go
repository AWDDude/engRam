package store

import (
	"path/filepath"
	"testing"
)

func TestModelDownloadDir(t *testing.T) {
	// Must match hugot's own naming, or we would create one directory and it
	// would write to another — leaving the fresh-install failure in place.
	cases := []struct {
		model string
		want  string
	}{
		{"KnightsAnalytics/all-MiniLM-L6-v2", "KnightsAnalytics_all-MiniLM-L6-v2"},
		{"BAAI/bge-small-en-v1.5", "BAAI_bge-small-en-v1.5"},
		{"simple-model", "simple-model"},
		{"org/model:revision", "org_model"}, // hugot drops everything after ":"
	}
	for _, c := range cases {
		got := modelDownloadDir("/models", c.model)
		if want := filepath.Join("/models", c.want); got != want {
			t.Errorf("modelDownloadDir(%q) = %q, want %q", c.model, got, want)
		}
	}
}
