package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
	"github.com/knights-analytics/hugot/util/fileutil"
)

// EmbeddingFunc turns text into a vector. Implementations are expected to
// return unit-normalized vectors (the hugot pipeline below does).
type EmbeddingFunc func(ctx context.Context, text string) ([]float32, error)

// modelDownloadDir returns the directory hugot will copy a model's files into,
// mirroring the naming in hugot's downloader: anything after a ":" is dropped
// and "/" becomes "_".
//
// We have to know this path because hugot's DownloadModel writes into it
// without creating it first, so the download fails on any machine that
// doesn't already have the directory — which is every fresh install.
func modelDownloadDir(modelDir, model string) string {
	name := model
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[:i]
	}
	return filepath.Join(modelDir, strings.ReplaceAll(name, "/", "_"))
}

// newEmbeddingFunc creates an embedding function backed by hugot's pure-Go
// (GoMLX simplego) session. The model is downloaded once to modelDir on first
// run and reused on subsequent starts.
//
// onnxFile names which .onnx file to take from the repo, relative to its root;
// most repos ship several variants and the download is rejected as ambiguous
// otherwise. hugot flattens the downloaded file to its basename on disk, which
// is what the pipeline is then pointed at.
//
// The returned cleanup func must be called when the process exits.
func newEmbeddingFunc(ctx context.Context, modelDir, model, onnxFile string) (EmbeddingFunc, func(), error) {
	// hugot resolves all file access through a FileSystem bound to the
	// context; DownloadModel fails with "no filesystem bound to context"
	// without this. A nil system selects hugot's default OS-backed one.
	ctx = fileutil.WithFileSystem(ctx, nil)

	session, err := hugot.NewGoSession(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("creating embedding session: %w", err)
	}
	cleanup := func() { _ = session.Destroy() }

	// modelDownloadDir and the OnnxFilename below both assume hugot's current
	// flatten-to-basename layout.
	modelPath := modelDownloadDir(modelDir, model)
	onnxPath := filepath.Join(modelPath, filepath.Base(onnxFile))

	// hugot.DownloadModel always hits the network to check the model's
	// revision, even when every file is already cached locally, so skip it
	// entirely once we can see the onnx file is already on disk.
	if _, err := os.Stat(onnxPath); err != nil {
		// Must exist before DownloadModel runs; see modelDownloadDir.
		if err := os.MkdirAll(modelPath, 0o755); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("creating model dir: %w", err)
		}

		opts := hugot.NewDownloadOptions()
		opts.OnnxFilePath = onnxFile

		modelPath, err = hugot.DownloadModel(ctx, model, modelDir, opts)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("downloading model %s: %w", model, err)
		}

		// Checking for the file here turns a hugot layout change into a
		// clear error instead of a confusing one surfacing later from
		// inside the pipeline/onnxruntime.
		onnxPath = filepath.Join(modelPath, filepath.Base(onnxFile))
		if _, err := os.Stat(onnxPath); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("downloaded model %s but expected onnx file not found at %s: %w", model, onnxPath, err)
		}
	}

	pipeline, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		Name:         "embedding",
		OnnxFilename: filepath.Base(onnxFile),
		Options:      []hugot.FeatureExtractionOption{pipelines.WithNormalization()},
	})
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("creating embedding pipeline: %w", err)
	}

	return func(ctx context.Context, text string) ([]float32, error) {
		out, err := pipeline.RunPipeline(ctx, []string{text})
		if err != nil {
			return nil, fmt.Errorf("embedding inference: %w", err)
		}
		if len(out.Embeddings) == 0 {
			return nil, fmt.Errorf("empty embedding output")
		}
		return out.Embeddings[0], nil
	}, cleanup, nil
}
