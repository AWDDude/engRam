package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	chromem "github.com/philippgille/chromem-go"

	"github.com/AWDDude/engRam/internal/config"
)

const (
	// chunkSizeWords is the max words per chunk (~200 tokens, safely below the
	// 256-token limit of all-MiniLM-L6-v2 after accounting for special tokens).
	chunkSizeWords = 200
	// chunkOverlapWords is the number of words shared between adjacent chunks
	// to preserve context at boundaries.
	chunkOverlapWords = 30
)

// Memory is the internal representation of a stored memory.
type Memory struct {
	ID        string   `json:"id"`
	Content   string   `json:"content"`
	Type      string   `json:"type"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"created_at"`
}

// MemoryResult extends Memory with a cosine similarity score from vector search.
type MemoryResult struct {
	Memory
	Score float32 `json:"score"`
}

// Store is the persistence interface for memories.
type Store interface {
	Add(ctx context.Context, content, memType string, tags []string) (string, error)
	Search(ctx context.Context, query string, minScore float32) ([]MemoryResult, error)
	List(ctx context.Context, typeFilter, tagFilter string, limit int) ([]Memory, error)
	GetByID(ctx context.Context, id string) (Memory, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id, content string) error
}

// chromemStore implements Store using chromem-go for vector search and a JSON
// sidecar (metaIndex) for listing and ID lookups without requiring a vector query.
type chromemStore struct {
	col  *chromem.Collection
	meta *metaIndex
}

// metaIndex is a lightweight JSON-persisted index of all memories used for
// listing and ID lookups. It stays in sync with the chromem collection.
type metaIndex struct {
	mu   sync.RWMutex
	docs map[string]Memory
	path string
}

func newMetaIndex(path string) (*metaIndex, error) {
	m := &metaIndex{
		docs: make(map[string]Memory),
		path: path,
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading meta index: %w", err)
	}
	if err := json.Unmarshal(data, &m.docs); err != nil {
		return nil, fmt.Errorf("parsing meta index: %w", err)
	}
	return m, nil
}

func (m *metaIndex) save() error {
	data, err := json.MarshalIndent(m.docs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0600)
}

func (m *metaIndex) set(mem Memory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[mem.ID] = mem
	return m.save()
}

func (m *metaIndex) get(id string) (Memory, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mem, ok := m.docs[id]
	return mem, ok
}

func (m *metaIndex) remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs, id)
	return m.save()
}

func (m *metaIndex) list(typeFilter, tagFilter string, limit int) []Memory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var results []Memory
	for _, mem := range m.docs {
		if typeFilter != "" && mem.Type != typeFilter {
			continue
		}
		if tagFilter != "" {
			matched := false
			for _, tag := range mem.Tags {
				if strings.Contains(strings.ToLower(tag), strings.ToLower(tagFilter)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		results = append(results, mem)
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results
}

// chunkText splits text into overlapping word-based chunks. Returns a
// single-element slice when text fits within chunkSizeWords.
func chunkText(text string) []string {
	words := strings.Fields(text)
	if len(words) <= chunkSizeWords {
		return []string{text}
	}
	stride := chunkSizeWords - chunkOverlapWords
	var chunks []string
	for start := 0; start < len(words); start += stride {
		end := start + chunkSizeWords
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[start:end], " "))
		if end == len(words) {
			break
		}
	}
	return chunks
}

// NewChromemStore creates a production store backed by hugot (GoMLX) for embeddings.
// The second return value is a cleanup func that must be called when the process exits.
// Returns ModelChangedError if the configured model differs from the one used to build
// the existing database — run 'engram migrate' to resolve.
func NewChromemStore(cfg config.Config) (Store, func(), error) {
	if err := os.MkdirAll(cfg.DB.Path, 0700); err != nil {
		return nil, nil, fmt.Errorf("creating db dir: %w", err)
	}

	meta, err := loadDBMeta(cfg.DB.Path)
	if err != nil {
		return nil, nil, err
	}
	if meta.ActiveModel == "" {
		if err := saveDBMeta(cfg.DB.Path, dbMeta{ActiveModel: cfg.Model.EmbeddingModel}); err != nil {
			return nil, nil, fmt.Errorf("writing db meta: %w", err)
		}
	} else if meta.ActiveModel != cfg.Model.EmbeddingModel {
		return nil, nil, &ModelChangedError{OldModel: meta.ActiveModel, NewModel: cfg.Model.EmbeddingModel}
	}

	embFn, cleanup, err := newEmbeddingFunc(context.Background(), cfg.Model.Path, cfg.Model.EmbeddingModel)
	if err != nil {
		return nil, nil, err
	}
	s, err := newChromemStoreWithEmb(cfg, embFn)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return s, cleanup, nil
}

// newChromemStoreWithEmb creates a store with an injectable embedding function.
// Exists to allow tests to inject a mock without requiring a live model.
func newChromemStoreWithEmb(cfg config.Config, embFn chromem.EmbeddingFunc) (Store, error) {
	collPath := modelCollectionPath(cfg.DB.Path, cfg.Model.EmbeddingModel)
	if err := os.MkdirAll(collPath, 0700); err != nil {
		return nil, fmt.Errorf("creating collection dir: %w", err)
	}

	db, err := chromem.NewPersistentDB(collPath, false)
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}

	col, err := db.GetOrCreateCollection("memories", nil, embFn)
	if err != nil {
		return nil, fmt.Errorf("creating collection: %w", err)
	}

	meta, err := newMetaIndex(filepath.Join(collPath, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("loading meta index: %w", err)
	}

	return &chromemStore{col: col, meta: meta}, nil
}

func (s *chromemStore) Add(ctx context.Context, content, memType string, tags []string) (string, error) {
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.addMemory(ctx, id, content, memType, tags, now); err != nil {
		return "", err
	}
	return id, nil
}

// addMemory stores a memory to the vector collection (with chunking for large
// content) and to the metaIndex. Used by Add, Update, and Migrate.
//
// Content that fits within chunkSizeWords is stored as a single chromem document
// with ID equal to the memory ID (backward-compatible with pre-chunking data).
// Larger content is split into overlapping chunks stored as separate chromem
// documents (IDs: "{id}:chunk:{n}") with a "parent_id" metadata field pointing
// back to the memory ID; the full content is always preserved in the metaIndex.
func (s *chromemStore) addMemory(ctx context.Context, id, content, memType string, tags []string, createdAt string) error {
	tagsJSON, _ := json.Marshal(tags)
	baseMeta := map[string]string{
		"type":       memType,
		"tags":       string(tagsJSON),
		"created_at": createdAt,
	}

	chunks := chunkText(content)
	if len(chunks) == 1 {
		if err := s.col.AddDocument(ctx, chromem.Document{
			ID:       id,
			Content:  content,
			Metadata: baseMeta,
		}); err != nil {
			return fmt.Errorf("adding to vector store: %w", err)
		}
	} else {
		chunkMeta := make(map[string]string, len(baseMeta)+1)
		for k, v := range baseMeta {
			chunkMeta[k] = v
		}
		chunkMeta["parent_id"] = id

		for i, chunk := range chunks {
			if err := s.col.AddDocument(ctx, chromem.Document{
				ID:       fmt.Sprintf("%s:chunk:%d", id, i),
				Content:  chunk,
				Metadata: chunkMeta,
			}); err != nil {
				// Clean up any chunks already written.
				_ = s.col.Delete(ctx, map[string]string{"parent_id": id}, nil)
				return fmt.Errorf("adding chunk %d to vector store: %w", i, err)
			}
		}
	}

	return s.meta.set(Memory{
		ID:        id,
		Content:   content,
		Type:      memType,
		Tags:      tags,
		CreatedAt: createdAt,
	})
}

// deleteChromemDocs removes all chromem documents belonging to a memory. It
// handles both the legacy single-document format (doc ID == memory ID) and the
// chunked format (docs with "parent_id" metadata == memory ID).
func (s *chromemStore) deleteChromemDocs(ctx context.Context, id string) error {
	if err := s.col.Delete(ctx, nil, nil, id); err != nil {
		return fmt.Errorf("deleting from vector store: %w", err)
	}
	return s.col.Delete(ctx, map[string]string{"parent_id": id}, nil)
}

func (s *chromemStore) Search(ctx context.Context, query string, minScore float32) ([]MemoryResult, error) {
	count := s.col.Count()
	if count == 0 {
		return []MemoryResult{}, nil
	}

	results, err := s.col.Query(ctx, query, count, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("querying vector store: %w", err)
	}

	// Deduplicate by memory ID: chunk results resolve to their parent, direct
	// results use their own ID. Keep the highest score per memory.
	type entry struct {
		mem   Memory
		score float32
	}
	seen := make(map[string]entry)

	for _, r := range results {
		if r.Similarity < minScore {
			continue
		}
		memID := r.ID
		if pid := r.Metadata["parent_id"]; pid != "" {
			memID = pid
		}
		mem, ok := s.meta.get(memID)
		if !ok {
			continue // orphaned chunk, skip
		}
		if e, exists := seen[memID]; !exists || r.Similarity > e.score {
			seen[memID] = entry{mem: mem, score: r.Similarity}
		}
	}

	out := make([]MemoryResult, 0, len(seen))
	for _, e := range seen {
		out = append(out, MemoryResult{Memory: e.mem, Score: e.score})
	}
	return out, nil
}

func (s *chromemStore) List(_ context.Context, typeFilter, tagFilter string, limit int) ([]Memory, error) {
	results := s.meta.list(typeFilter, tagFilter, limit)
	if results == nil {
		return []Memory{}, nil
	}
	return results, nil
}

func (s *chromemStore) GetByID(_ context.Context, id string) (Memory, error) {
	mem, ok := s.meta.get(id)
	if !ok {
		return Memory{}, fmt.Errorf("memory %q not found", id)
	}
	return mem, nil
}

func (s *chromemStore) Delete(ctx context.Context, id string) error {
	if _, err := s.GetByID(ctx, id); err != nil {
		return err
	}
	if err := s.deleteChromemDocs(ctx, id); err != nil {
		return err
	}
	return s.meta.remove(id)
}

func (s *chromemStore) Update(ctx context.Context, id, content string) error {
	existing, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.deleteChromemDocs(ctx, id); err != nil {
		return err
	}
	return s.addMemory(ctx, id, content, existing.Type, existing.Tags, existing.CreatedAt)
}
