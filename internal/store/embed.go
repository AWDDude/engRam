package store

import (
	"context"
	"fmt"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
	chromem "github.com/philippgille/chromem-go"
)

// newEmbeddingFunc creates a chromem-compatible embedding function backed by
// hugot's pure-Go (GoMLX simplego) session. The model is downloaded once to
// modelDir on first run and reused on subsequent starts.
// The returned cleanup func must be called when the process exits.
func newEmbeddingFunc(ctx context.Context, modelDir, model string) (chromem.EmbeddingFunc, func(), error) {
	session, err := hugot.NewGoSession(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("creating embedding session: %w", err)
	}
	cleanup := func() { _ = session.Destroy() }

	modelPath, err := hugot.DownloadModel(ctx, model, modelDir, hugot.NewDownloadOptions())
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("downloading model %s: %w", model, err)
	}

	pipeline, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		Name:         "embedding",
		OnnxFilename: "model.onnx",
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
