package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	LinkedIDs []string `json:"linked_ids"`
	CreatedAt string   `json:"created_at"`
}

// SearchResult is the lightweight projection returned by Search, and reused
// for the linked-memory summaries embedded in RetrieveResult.
type SearchResult struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
}

// RetrieveResult is the full-detail response for the retrieve tool: the
// memory itself plus a one-level summary of each memory it links to.
type RetrieveResult struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Content   string         `json:"content"`
	Tags      []string       `json:"tags"`
	CreatedAt string         `json:"created_at"`
	Linked    []SearchResult `json:"linked"`
}

// MemoryUpdate is a patch: nil fields are left untouched, non-nil fields are
// applied as given (including an explicit empty slice, which clears tags or
// linked_ids).
type MemoryUpdate struct {
	Title     *string
	Content   *string
	Tags      *[]string
	LinkedIDs *[]string
}

// Store is the persistence interface for memories.
type Store interface {
	Add(ctx context.Context, title, content string, tags, linkedIDs []string) (string, error)
	Search(ctx context.Context, query, tagFilter string, minScore float32, limit int) ([]SearchResult, error)
	GetByID(ctx context.Context, id string) (Memory, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id string, patch MemoryUpdate) error
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

func (m *metaIndex) list(tagFilter string, limit int) []Memory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var results []Memory
	for _, mem := range m.docs {
		if tagFilter != "" && !hasMatchingTag(mem.Tags, tagFilter) {
			continue
		}
		results = append(results, mem)
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results
}

// getRaw, setRaw, and removeRaw mutate docs without locking or persisting.
// Callers must hold m.mu (getRaw needs at least a read lock; setRaw/removeRaw
// need the write lock) and are responsible for calling save() themselves —
// they exist so a batch operation that touches several records (see
// syncLinks/cascadeUnlink below) can do so under one withLock call.

func (m *metaIndex) getRaw(id string) (Memory, bool) {
	mem, ok := m.docs[id]
	return mem, ok
}

func (m *metaIndex) setRaw(mem Memory) {
	m.docs[mem.ID] = mem
}

func (m *metaIndex) removeRaw(id string) {
	delete(m.docs, id)
}

// withLock runs fn while holding the write lock, then persists once if fn
// returns nil. This is the single choke point multi-record mutations go
// through so each logical operation gets exactly one save() call.
func (m *metaIndex) withLock(fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	return m.save()
}

// hasMatchingTag reports whether any tag case-insensitively contains filter.
func hasMatchingTag(tags []string, filter string) bool {
	for _, tag := range tags {
		if strings.Contains(strings.ToLower(tag), strings.ToLower(filter)) {
			return true
		}
	}
	return false
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
// the existing database — run 'engram reembed' to resolve.
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

func (s *chromemStore) Add(ctx context.Context, title, content string, tags, linkedIDs []string) (string, error) {
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.addMemory(ctx, id, title, content, tags, nil, now); err != nil {
		return "", err
	}
	if len(linkedIDs) > 0 {
		if _, err := s.syncLinks(id, linkedIDs); err != nil {
			// The memory itself was already persisted above; only the link
			// side failed (e.g. an invalid linked ID). Return the ID so the
			// caller isn't left with an orphaned record they can't address.
			return id, fmt.Errorf("memory created but linking failed: %w", err)
		}
	}
	return id, nil
}

// addMemory stores a memory to the vector collection (with chunking for large
// content) and to the metaIndex. Used by Add, Update, CSV import, and Reembed.
//
// The embedded text is "title\n\ncontent" rather than content alone, so
// semantic search matches against the title as well — chromem-go embeds a
// single Content field per document, so this is the simplest way to cover
// both without a second index. Memory.Content in the metaIndex still stores
// raw content only; the combined text is embedding input, never surfaced back.
//
// Content that fits within chunkSizeWords is stored as a single chromem document
// with ID equal to the memory ID (backward-compatible with pre-chunking data).
// Larger content is split into overlapping chunks stored as separate chromem
// documents (IDs: "{id}:chunk:{n}") with a "parent_id" metadata field pointing
// back to the memory ID; the full content is always preserved in the metaIndex.
//
// linkedIDs is written to the metaIndex verbatim with no validation or
// bidirectional sync — callers that need the link invariant enforced (Add,
// Update) go through syncLinks separately; CSV import and Reembed intentionally
// bypass it and restore linkedIDs as-is (see import.go).
func (s *chromemStore) addMemory(ctx context.Context, id, title, content string, tags, linkedIDs []string, createdAt string) error {
	tagsJSON, _ := json.Marshal(tags)
	baseMeta := map[string]string{
		// Best-effort/write-only, same as today: nothing reads tags/created_at
		// back out of chromem metadata, metaIndex is the source of truth.
		"tags":       string(tagsJSON),
		"created_at": createdAt,
	}

	embedText := title + "\n\n" + content
	chunks := chunkText(embedText)
	if len(chunks) == 1 {
		if err := s.col.AddDocument(ctx, chromem.Document{
			ID:       id,
			Content:  embedText,
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
		Title:     title,
		Content:   content,
		Tags:      tags,
		LinkedIDs: linkedIDs,
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

// Search performs a semantic query, a tag filter, or both. query == ""
// switches to a tag-only scan (mirroring the old List behavior) — callers
// outside the MCP layer (export, reembed) may also pass both query and
// tagFilter empty to mean "everything"; the "at least one required" rule is
// enforced by the MCP handler, not here. limit <= 0 means unlimited.
func (s *chromemStore) Search(ctx context.Context, query, tagFilter string, minScore float32, limit int) ([]SearchResult, error) {
	if query == "" {
		return toSearchResults(s.meta.list(tagFilter, limit)), nil
	}

	count := s.col.Count()
	if count == 0 {
		return []SearchResult{}, nil
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
		if tagFilter != "" && !hasMatchingTag(mem.Tags, tagFilter) {
			continue
		}
		if e, exists := seen[memID]; !exists || r.Similarity > e.score {
			seen[memID] = entry{mem: mem, score: r.Similarity}
		}
	}

	ordered := make([]entry, 0, len(seen))
	for _, e := range seen {
		ordered = append(ordered, e)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].score > ordered[j].score })
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[:limit]
	}

	out := make([]SearchResult, 0, len(ordered))
	for _, e := range ordered {
		out = append(out, SearchResult{ID: e.mem.ID, Title: e.mem.Title, Tags: e.mem.Tags})
	}
	return out, nil
}

func toSearchResults(mems []Memory) []SearchResult {
	out := make([]SearchResult, 0, len(mems))
	for _, mem := range mems {
		out = append(out, SearchResult{ID: mem.ID, Title: mem.Title, Tags: mem.Tags})
	}
	return out
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
	return s.cascadeUnlink(id)
}

func (s *chromemStore) Update(ctx context.Context, id string, patch MemoryUpdate) error {
	existing, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}

	linkedIDs := existing.LinkedIDs
	if patch.LinkedIDs != nil {
		linkedIDs, err = s.syncLinks(id, *patch.LinkedIDs)
		if err != nil {
			return err
		}
	}

	title := existing.Title
	if patch.Title != nil {
		title = *patch.Title
	}
	content := existing.Content
	if patch.Content != nil {
		content = *patch.Content
	}
	tags := existing.Tags
	if patch.Tags != nil {
		tags = *patch.Tags
	}

	if patch.Title == nil && patch.Content == nil {
		// No re-embed needed. linked_ids (if patched) was already persisted
		// by syncLinks above; only need to persist here if tags also changed.
		if patch.Tags == nil {
			return nil
		}
		existing.Tags = tags
		existing.LinkedIDs = linkedIDs
		return s.meta.set(existing)
	}

	if err := s.deleteChromemDocs(ctx, id); err != nil {
		return err
	}
	return s.addMemory(ctx, id, title, content, tags, linkedIDs, existing.CreatedAt)
}

// normalizeLinks dedupes ids and drops any reference to selfID, preserving
// first-seen order.
func normalizeLinks(selfID string, ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == selfID || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func toSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func removeString(ids []string, target string) []string {
	out := ids[:0:0]
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}

// syncLinks normalizes newLinks (self-reference and duplicates stripped),
// validates every remaining ID exists, then atomically updates id's
// LinkedIDs and every peer whose LinkedIDs must change to keep the
// relationship bidirectional. Returns the normalized link set that was
// applied. On validation failure nothing is mutated.
//
// Validation and mutation happen inside one withLock call so a concurrent
// delete of a link target can't race a separate validate-then-mutate window —
// this is the detail that makes the single metaIndex mutex sufficient for
// correctness under concurrent link operations.
func (s *chromemStore) syncLinks(id string, newLinks []string) ([]string, error) {
	normalized := normalizeLinks(id, newLinks)

	err := s.meta.withLock(func() error {
		target, ok := s.meta.getRaw(id)
		if !ok {
			return fmt.Errorf("memory %q not found", id)
		}
		for _, linkID := range normalized {
			if _, ok := s.meta.getRaw(linkID); !ok {
				return fmt.Errorf("linked memory %q not found", linkID)
			}
		}

		oldSet := toSet(target.LinkedIDs)
		newSet := toSet(normalized)

		for _, peerID := range normalized {
			if oldSet[peerID] {
				continue // already linked, peer side already has id
			}
			peer, _ := s.meta.getRaw(peerID)
			if !toSet(peer.LinkedIDs)[id] {
				peer.LinkedIDs = append(peer.LinkedIDs, id)
				s.meta.setRaw(peer)
			}
		}
		for _, peerID := range target.LinkedIDs {
			if newSet[peerID] {
				continue // still linked, leave the peer alone
			}
			peer, ok := s.meta.getRaw(peerID)
			if !ok {
				continue // peer already gone, nothing to clean up
			}
			peer.LinkedIDs = removeString(peer.LinkedIDs, id)
			s.meta.setRaw(peer)
		}

		target.LinkedIDs = normalized
		s.meta.setRaw(target)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

// cascadeUnlink removes id from every other memory's LinkedIDs and deletes
// id's own metaIndex entry, all under one lock/save so the cleanup is atomic
// with the delete itself — no dangling references are ever left behind.
func (s *chromemStore) cascadeUnlink(id string) error {
	return s.meta.withLock(func() error {
		target, ok := s.meta.getRaw(id)
		if !ok {
			return fmt.Errorf("memory %q not found", id)
		}
		for _, peerID := range target.LinkedIDs {
			peer, ok := s.meta.getRaw(peerID)
			if !ok {
				continue
			}
			peer.LinkedIDs = removeString(peer.LinkedIDs, id)
			s.meta.setRaw(peer)
		}
		s.meta.removeRaw(id)
		return nil
	})
}
