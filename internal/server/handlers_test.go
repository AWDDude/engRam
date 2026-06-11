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

// mockStore is an in-memory Store implementation for handler tests.
type mockStore struct {
	memories map[string]store.Memory
	counter  int
	listErr  error
}

func newMockStore() *mockStore {
	return &mockStore{memories: make(map[string]store.Memory)}
}

func (m *mockStore) Add(_ context.Context, content, memType string, tags []string) (string, error) {
	m.counter++
	id := fmt.Sprintf("test-id-%d", m.counter)
	m.memories[id] = store.Memory{
		ID:        id,
		Content:   content,
		Type:      memType,
		Tags:      tags,
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	return id, nil
}

func (m *mockStore) Search(_ context.Context, query string, limit int, typeFilter string) ([]store.MemoryResult, error) {
	var results []store.MemoryResult
	for _, mem := range m.memories {
		if typeFilter != "" && mem.Type != typeFilter {
			continue
		}
		if strings.Contains(mem.Content, query) {
			results = append(results, store.MemoryResult{Memory: mem, Score: 0.9})
		}
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (m *mockStore) List(_ context.Context, typeFilter, tagFilter string, limit int) ([]store.Memory, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var results []store.Memory
	for _, mem := range m.memories {
		if typeFilter != "" && mem.Type != typeFilter {
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
		results = append(results, mem)
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

func (m *mockStore) Update(_ context.Context, id, content string) error {
	mem, ok := m.memories[id]
	if !ok {
		return fmt.Errorf("memory %q not found", id)
	}
	mem.Content = content
	m.memories[id] = mem
	return nil
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
		"content": "David uses a standing desk",
		"type":    "preference",
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

func TestHandleStoreMemory_TaskType(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{
		"content": "refactor the auth module",
		"type":    "task",
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success for task type, got error: %v", result.Content)
	}
}

func TestHandleStoreMemory_ActionType(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{
		"content": "created PR #42 to fix the login bug",
		"type":    "action",
		"tags":    []any{"pr", "bugfix"},
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success for action type, got error: %v", result.Content)
	}
}

func TestHandleStoreMemory_InvalidType(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{
		"content": "some content",
		"type":    "work_context",
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid type")
	}
}

func TestHandleStoreMemory_MissingContent(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"type": "fact"})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing content")
	}
}

func TestHandleStoreMemory_MissingType(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"content": "some content"})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing type")
	}
}

// --- search ---

func TestHandleSearchMemory_Success(t *testing.T) {
	ms := newMockStore()
	ms.memories["x"] = store.Memory{ID: "x", Content: "standing desk preference", Type: "preference"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"query": "standing desk"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}

	var results []store.MemoryResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &results); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result")
	}
}

func TestHandleSearchMemory_MissingQuery(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{})

	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing query")
	}
}

func TestHandleSearchMemory_NoLimitReturnsAll(t *testing.T) {
	ms := newMockStore()
	for i := 0; i < 10; i++ {
		ms.memories[fmt.Sprintf("id-%d", i)] = store.Memory{
			ID: fmt.Sprintf("id-%d", i), Content: "item", Type: "fact",
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

	var results []store.MemoryResult
	json.Unmarshal([]byte(resultText(t, result)), &results) //nolint:errcheck
	if len(results) != 10 {
		t.Errorf("expected all 10 results with no limit, got %d", len(results))
	}
}

func TestHandleSearchMemory_WithLimit(t *testing.T) {
	ms := newMockStore()
	for i := 0; i < 10; i++ {
		ms.memories[fmt.Sprintf("id-%d", i)] = store.Memory{
			ID: fmt.Sprintf("id-%d", i), Content: "item", Type: "fact",
		}
	}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"query": "item", "limit": 3})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var results []store.MemoryResult
	json.Unmarshal([]byte(resultText(t, result)), &results) //nolint:errcheck
	if len(results) != 3 {
		t.Errorf("expected 3 results with limit=3, got %d", len(results))
	}
}

// --- list ---

func TestHandleListMemories_ReturnsAll(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Content: "one", Type: "fact"}
	ms.memories["b"] = store.Memory{ID: "b", Content: "two", Type: "preference"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{})
	result, err := app.handleListMemories(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var mems []store.Memory
	if err := json.Unmarshal([]byte(resultText(t, result)), &mems); err != nil {
		t.Fatalf("parsing list result: %v", err)
	}
	if len(mems) != 2 {
		t.Errorf("expected 2 memories, got %d", len(mems))
	}
}

func TestHandleListMemories_TypeFilter(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Content: "a fact", Type: "fact"}
	ms.memories["b"] = store.Memory{ID: "b", Content: "an action", Type: "action"}
	ms.memories["c"] = store.Memory{ID: "c", Content: "a task", Type: "task"}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"type_filter": "action"})
	result, err := app.handleListMemories(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var mems []store.Memory
	if err := json.Unmarshal([]byte(resultText(t, result)), &mems); err != nil {
		t.Fatalf("parsing list result: %v", err)
	}
	if len(mems) != 1 || mems[0].Type != "action" {
		t.Errorf("expected 1 action memory, got %d (%v)", len(mems), mems)
	}
}

func TestHandleListMemories_TagFilter(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Content: "kubernetes fact", Type: "fact", Tags: []string{"kubernetes", "infra"}}
	ms.memories["b"] = store.Memory{ID: "b", Content: "go preference", Type: "preference", Tags: []string{"golang"}}
	app := &App{store: ms}

	req := makeRequest(map[string]any{"tag_filter": "kubernetes"})
	result, err := app.handleListMemories(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var mems []store.Memory
	if err := json.Unmarshal([]byte(resultText(t, result)), &mems); err != nil {
		t.Fatalf("parsing list result: %v", err)
	}
	if len(mems) != 1 || mems[0].ID != "a" {
		t.Errorf("expected 1 kubernetes memory, got %d", len(mems))
	}
}

func TestHandleListMemories_StoreError(t *testing.T) {
	ms := newMockStore()
	ms.listErr = fmt.Errorf("disk failure")
	app := &App{store: ms}

	req := makeRequest(map[string]any{})
	result, err := app.handleListMemories(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result when store returns error")
	}
}

// --- delete ---

func TestHandleDeleteMemory_Success(t *testing.T) {
	ms := newMockStore()
	ms.memories["del-id"] = store.Memory{ID: "del-id", Content: "delete me", Type: "fact"}
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

// --- update ---

func TestHandleUpdateMemory_Success(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Content: "old content", Type: "fact"}
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

func TestHandleUpdateMemory_MissingContent(t *testing.T) {
	app := &App{store: newMockStore()}
	req := makeRequest(map[string]any{"memory_id": "some-id"})

	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing content")
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
