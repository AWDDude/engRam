package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/AWDDude/engRam/internal/store"
)

// mockStore is an in-memory Store implementation for handler tests. It does
// not enforce the bidirectional-link invariant — that's proven at the store
// package level (internal/store/links_test.go); handler tests only need to
// verify args are threaded through correctly.
type mockStore struct {
	memories  map[string]store.Memory
	counter   int
	searchErr error
}

func newMockStore() *mockStore {
	return &mockStore{memories: make(map[string]store.Memory)}
}

func (m *mockStore) Add(_ context.Context, title, content string, tags, linkedIDs []string) (string, error) {
	m.counter++
	id := fmt.Sprintf("test-id-%d", m.counter)
	m.memories[id] = store.Memory{
		ID:        id,
		Title:     title,
		Content:   content,
		Tags:      tags,
		LinkedIDs: linkedIDs,
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	return id, nil
}

func (m *mockStore) Search(_ context.Context, query, tagFilter string, _ float32, limit int) ([]store.SearchResult, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	var results []store.SearchResult
	for _, mem := range m.memories {
		if query != "" && !strings.Contains(mem.Content, query) && !strings.Contains(mem.Title, query) {
			continue
		}
		if tagFilter != "" {
			matched := false
			for _, tag := range mem.Tags {
				if strings.Contains(tag, tagFilter) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		results = append(results, store.SearchResult{ID: mem.ID, Title: mem.Title, Tags: mem.Tags})
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (m *mockStore) GetByID(_ context.Context, id string) (store.Memory, error) {
	mem, ok := m.memories[id]
	if !ok {
		return store.Memory{}, fmt.Errorf("memory %q not found", id)
	}
	return mem, nil
}

func (m *mockStore) Delete(_ context.Context, id string) error {
	if _, ok := m.memories[id]; !ok {
		return fmt.Errorf("memory %q not found", id)
	}
	delete(m.memories, id)
	return nil
}

func (m *mockStore) Update(_ context.Context, id string, patch store.MemoryUpdate) error {
	mem, ok := m.memories[id]
	if !ok {
		return fmt.Errorf("memory %q not found", id)
	}
	if patch.Title != nil {
		mem.Title = *patch.Title
	}
	if patch.Content != nil {
		mem.Content = *patch.Content
	}
	if patch.Tags != nil {
		mem.Tags = *patch.Tags
	}
	if patch.LinkedIDs != nil {
		mem.LinkedIDs = *patch.LinkedIDs
	}
	m.memories[id] = mem
	return nil
}

// captureSearchArgsStore wraps mockStore to capture the minScore/limit passed to Search.
type captureSearchArgsStore struct {
	*mockStore
	onSearch func(minScore float32, limit int)
}

func (c *captureSearchArgsStore) Search(ctx context.Context, query, tagFilter string, minScore float32, limit int) ([]store.SearchResult, error) {
	c.onSearch(minScore, limit)
	return c.mockStore.Search(ctx, query, tagFilter, minScore, limit)
}

// makeRequest builds a CallToolRequest with the given arguments map.
func makeRequest(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	return req
}

// --- store ---

func TestHandleStoreMemory_Success(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{
		"title":   "Standing desk preference",
		"content": "David uses a standing desk",
		"tags":    []any{"desk", "ergonomics"},
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}
	if !strings.Contains(resultText(t, result), "stored") {
		t.Errorf("expected 'stored' in response, got: %s", resultText(t, result))
	}
}

func TestHandleStoreMemory_WithLinkedIDs(t *testing.T) {
	ms := newMockStore()
	ms.memories["existing"] = store.Memory{ID: "existing", Title: "Existing", Content: "existing content"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{
		"title":      "New memory",
		"content":    "new content",
		"linked_ids": []any{"existing"},
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}

	found := false
	for _, mem := range ms.memories {
		if mem.Title == "New memory" {
			found = true
			if len(mem.LinkedIDs) != 1 || mem.LinkedIDs[0] != "existing" {
				t.Errorf("expected linked_ids threaded through, got %v", mem.LinkedIDs)
			}
		}
	}
	if !found {
		t.Fatal("stored memory not found")
	}
}

func TestHandleStoreMemory_TitleRequired(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"content": "some content"})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing title")
	}
}

func TestHandleStoreMemory_TitleTooLong(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{
		"title":   strings.Repeat("x", 101),
		"content": "some content",
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for a title over 100 characters")
	}
}

func TestHandleStoreMemory_TitleExactly100_OK(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{
		"title":   strings.Repeat("x", 100),
		"content": "some content",
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected a 100-character title to be accepted, got error: %v", result.Content)
	}
}

func TestHandleStoreMemory_UnicodeTitleRuneCount(t *testing.T) {
	// 100 multi-byte runes (é is 2 bytes in UTF-8) — must be accepted since
	// the cap is rune-counted, not byte-counted.
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{
		"title":   strings.Repeat("é", 100),
		"content": "some content",
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected a 100-rune unicode title to be accepted, got error: %v", result.Content)
	}
}

func TestHandleStoreMemory_MissingContent(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"title": "Some title"})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing content")
	}
}

// --- search ---

func TestHandleSearchMemory_Success(t *testing.T) {
	ms := newMockStore()
	ms.memories["x"] = store.Memory{ID: "x", Title: "Standing desk", Content: "standing desk preference"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"query": "standing desk"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}

	var results []store.SearchResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &results); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result")
	}
}

func TestHandleSearchMemory_ResultShapeOnlyIDTitleTags(t *testing.T) {
	ms := newMockStore()
	ms.memories["x"] = store.Memory{ID: "x", Title: "Title", Content: "some content here", Tags: []string{"a"}}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"query": "content"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	text := resultText(t, result)
	if strings.Contains(text, "\"content\"") {
		t.Errorf("expected search results to omit content, got: %s", text)
	}
	if strings.Contains(text, "\"score\"") {
		t.Errorf("expected search results to omit score, got: %s", text)
	}
}

func TestHandleSearchMemory_TagFilterOnly(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "Kubernetes fact", Content: "kubernetes fact", Tags: []string{"kubernetes", "infra"}}
	ms.memories["b"] = store.Memory{ID: "b", Title: "Go preference", Content: "go preference", Tags: []string{"golang"}}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"tag_filter": "kubernetes"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var results []store.SearchResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &results); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(results) != 1 || results[0].ID != "a" {
		t.Errorf("expected 1 kubernetes memory, got %d", len(results))
	}
}

func TestHandleSearchMemory_RequiresQueryOrTagFilter(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{})

	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result when neither query nor tag_filter is given")
	}
}

func TestHandleSearchMemory_TagFilterOnly_MinScoreProvidedNoError(t *testing.T) {
	// min_score only has meaning with a query — when tag_filter alone is used,
	// a provided min_score should simply be ignored, not rejected.
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "Tagged", Content: "content", Tags: []string{"x"}}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"tag_filter": "x", "min_score": 0.9})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected min_score to be harmlessly ignored without a query, got error: %v", result.Content)
	}
}

func TestHandleSearchMemory_ReturnsResults(t *testing.T) {
	ms := newMockStore()
	for i := 0; i < 10; i++ {
		ms.memories[fmt.Sprintf("id-%d", i)] = store.Memory{
			ID: fmt.Sprintf("id-%d", i), Title: "Item", Content: "item",
		}
	}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"query": "item"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var results []store.SearchResult
	json.Unmarshal([]byte(resultText(t, result)), &results) //nolint:errcheck
	if len(results) != 10 {
		t.Errorf("expected 10 results, got %d", len(results))
	}
}

func TestHandleSearchMemory_CustomMinScore(t *testing.T) {
	var capturedScore float32
	ms := newMockStore()
	ms.memories["id-1"] = store.Memory{ID: "id-1", Title: "Item", Content: "item"}

	app := &App{store: &captureSearchArgsStore{mockStore: ms, onSearch: func(s float32, _ int) { capturedScore = s }}}

	minScore := 0.8
	req := makeRequest(map[string]any{"query": "item", "min_score": minScore})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if capturedScore != float32(minScore) {
		t.Errorf("expected min_score %.1f, got %.1f", minScore, capturedScore)
	}
}

func TestHandleSearchMemory_DefaultMinScore(t *testing.T) {
	var capturedScore float32
	ms := newMockStore()
	ms.memories["id-1"] = store.Memory{ID: "id-1", Title: "Item", Content: "item"}

	app := NewApp(&captureSearchArgsStore{mockStore: ms, onSearch: func(s float32, _ int) { capturedScore = s }}, 0.7, 20)
	req := makeRequest(map[string]any{"query": "item"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if capturedScore != 0.7 {
		t.Errorf("expected default min_score 0.7, got %.2f", capturedScore)
	}
}

func TestHandleSearchMemory_LimitDefault(t *testing.T) {
	var capturedLimit int
	ms := newMockStore()
	ms.memories["id-1"] = store.Memory{ID: "id-1", Title: "Item", Content: "item"}

	app := NewApp(&captureSearchArgsStore{mockStore: ms, onSearch: func(_ float32, l int) { capturedLimit = l }}, 0.5, 20)
	req := makeRequest(map[string]any{"query": "item"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if capturedLimit != 20 {
		t.Errorf("expected default limit 20, got %d", capturedLimit)
	}
}

func TestHandleSearchMemory_LimitExplicitZeroMeansUnlimited(t *testing.T) {
	var capturedLimit int
	ms := newMockStore()
	ms.memories["id-1"] = store.Memory{ID: "id-1", Title: "Item", Content: "item"}

	app := NewApp(&captureSearchArgsStore{mockStore: ms, onSearch: func(_ float32, l int) { capturedLimit = l }}, 0.5, 20)
	limit := 0
	req := makeRequest(map[string]any{"query": "item", "limit": limit})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if capturedLimit != 0 {
		t.Errorf("expected explicit limit 0 to override the configured default, got %d", capturedLimit)
	}
}

func TestHandleSearchMemory_LimitNegative(t *testing.T) {
	var capturedLimit int
	ms := newMockStore()
	ms.memories["id-1"] = store.Memory{ID: "id-1", Title: "Item", Content: "item"}

	app := NewApp(&captureSearchArgsStore{mockStore: ms, onSearch: func(_ float32, l int) { capturedLimit = l }}, 0.5, 20)
	limit := -1
	req := makeRequest(map[string]any{"query": "item", "limit": limit})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if capturedLimit != -1 {
		t.Errorf("expected negative limit passed through as-is, got %d", capturedLimit)
	}
}

func TestHandleSearchMemory_StoreError(t *testing.T) {
	ms := newMockStore()
	ms.searchErr = fmt.Errorf("disk failure")
	app := &App{store: ms}

	req := makeRequest(map[string]any{"query": "anything"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result when store returns error")
	}
}

// --- retrieve ---

func TestHandleRetrieveMemory_Success(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "content a", Tags: []string{"t1"}, CreatedAt: "2026-01-01T00:00:00Z"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "a"})
	result, err := app.handleRetrieveMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var got store.RetrieveResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &got); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	if got.ID != "a" || got.Title != "A" || got.Content != "content a" {
		t.Errorf("unexpected retrieve result: %+v", got)
	}
}

func TestHandleRetrieveMemory_ExpandsLinkedSummaries(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "content a", LinkedIDs: []string{"b"}}
	ms.memories["b"] = store.Memory{ID: "b", Title: "B", Content: "content b", Tags: []string{"tag-b"}}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "a"})
	result, err := app.handleRetrieveMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var got store.RetrieveResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &got); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	if len(got.Linked) != 1 || got.Linked[0].ID != "b" || got.Linked[0].Title != "B" {
		t.Errorf("expected linked summary for B, got %+v", got.Linked)
	}
	if len(got.Linked[0].Tags) != 1 || got.Linked[0].Tags[0] != "tag-b" {
		t.Errorf("expected linked summary tags, got %v", got.Linked[0].Tags)
	}
}

func TestHandleRetrieveMemory_SkipsDanglingLinkGracefully(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "content a", LinkedIDs: []string{"ghost"}}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "a"})
	result, err := app.handleRetrieveMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected a dangling link to be skipped, not error the whole retrieve: %v", result.Content)
	}

	var got store.RetrieveResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &got); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	if len(got.Linked) != 0 {
		t.Errorf("expected dangling link skipped, got %v", got.Linked)
	}
}

func TestHandleRetrieveMemory_NotFound(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"memory_id": "ghost"})

	result, err := app.handleRetrieveMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for nonexistent memory")
	}
}

func TestHandleRetrieveMemory_MissingID(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{})

	result, err := app.handleRetrieveMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing memory_id")
	}
}

// --- delete ---

func TestHandleDeleteMemory_Success(t *testing.T) {
	ms := newMockStore()
	ms.memories["del-id"] = store.Memory{ID: "del-id", Title: "Delete me", Content: "delete me"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "del-id"})
	result, err := app.handleDeleteMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if _, ok := ms.memories["del-id"]; ok {
		t.Error("memory should be deleted from store")
	}
}

func TestHandleDeleteMemory_MissingID(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{})

	result, err := app.handleDeleteMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing memory_id")
	}
}

func TestHandleDeleteMemory_StoreError(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"memory_id": "nonexistent"})

	result, err := app.handleDeleteMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result when deleting nonexistent memory")
	}
}

// --- update ---

func TestHandleUpdateMemory_ContentOnly(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "old content"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "upd-id", "content": "new content"})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if ms.memories["upd-id"].Content != "new content" {
		t.Errorf("expected updated content, got %q", ms.memories["upd-id"].Content)
	}
	if ms.memories["upd-id"].Title != "Title" {
		t.Errorf("expected title preserved, got %q", ms.memories["upd-id"].Title)
	}
}

func TestHandleUpdateMemory_TitleOnly(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Old title", Content: "content"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "upd-id", "title": "New title"})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if ms.memories["upd-id"].Title != "New title" {
		t.Errorf("expected updated title, got %q", ms.memories["upd-id"].Title)
	}
	if ms.memories["upd-id"].Content != "content" {
		t.Errorf("expected content preserved, got %q", ms.memories["upd-id"].Content)
	}
}

func TestHandleUpdateMemory_TagsOnlyPatch(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content", Tags: []string{"old"}}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "upd-id", "tags": []any{"new"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if len(ms.memories["upd-id"].Tags) != 1 || ms.memories["upd-id"].Tags[0] != "new" {
		t.Errorf("expected tags replaced, got %v", ms.memories["upd-id"].Tags)
	}
}

func TestHandleUpdateMemory_LinkedIDsOnlyPatch(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "a"}
	ms.memories["b"] = store.Memory{ID: "b", Title: "B", Content: "b"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "a", "linked_ids": []any{"b"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if len(ms.memories["a"].LinkedIDs) != 1 || ms.memories["a"].LinkedIDs[0] != "b" {
		t.Errorf("expected linked_ids applied, got %v", ms.memories["a"].LinkedIDs)
	}
}

func TestHandleUpdateMemory_RequiresAtLeastOneField(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "upd-id"})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result when no fields are provided")
	}
}

func TestHandleUpdateMemory_TitlePatchValidatesLength(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "upd-id", "title": strings.Repeat("x", 101)})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for a title over 100 characters")
	}
}

func TestHandleUpdateMemory_EmptyContentRejected(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"memory_id": "upd-id", "content": ""})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for an explicitly empty content patch")
	}
}

func TestHandleUpdateMemory_NotFound(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"memory_id": "ghost", "content": "new"})

	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for nonexistent memory")
	}
}

func TestHandleUpdateMemory_MissingID(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"content": "new"})

	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing memory_id")
	}
}

// resultText extracts the text from the first content item in a tool result.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("result has no content")
	}
	tc, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	return tc.Text
}
