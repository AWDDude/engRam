package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	addErr    error
	updateErr error
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
	if m.addErr != nil {
		// Mirrors the real store: a failed Add commits nothing, so there is no
		// id to report back.
		delete(m.memories, id)
		return "", m.addErr
	}
	return id, nil
}

func (m *mockStore) Search(_ context.Context, query string, tagFilter []string, limit, offset int) ([]store.SearchResult, int, error) {
	if m.searchErr != nil {
		return nil, 0, m.searchErr
	}
	var matched []store.SearchResult
	for _, mem := range m.memories {
		if query != "" && !strings.Contains(mem.Content, query) && !strings.Contains(mem.Title, query) {
			continue
		}
		if !hasAllTagsForTest(mem.Tags, tagFilter) {
			continue
		}
		matched = append(matched, store.SearchResult{ID: mem.ID, Title: mem.Title, Tags: mem.Tags})
	}
	total := len(matched)
	if offset > 0 {
		if offset >= len(matched) {
			matched = nil
		} else {
			matched = matched[offset:]
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, total, nil
}

// hasAllTagsForTest mirrors store.hasAllTags's production semantics (exact,
// case-insensitive, AND across multiple filters) without exporting it.
func hasAllTagsForTest(tags, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	have := make(map[string]bool, len(tags))
	for _, tag := range tags {
		have[strings.ToLower(tag)] = true
	}
	for _, f := range filters {
		if !have[strings.ToLower(f)] {
			return false
		}
	}
	return true
}

func dedupeStringsForTest(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func (m *mockStore) Tags(_ context.Context) ([]string, error) {
	seen := make(map[string]bool)
	for _, mem := range m.memories {
		for _, tag := range mem.Tags {
			seen[tag] = true
		}
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags, nil
}

func (m *mockStore) GetByID(_ context.Context, id string) (store.Memory, error) {
	mem, ok := m.memories[id]
	if !ok {
		return store.Memory{}, fmt.Errorf("memory %q not found", id)
	}
	return mem, nil
}

// Retrieve mirrors the real store's assembly of a memory plus its linked
// summaries, including the skips for a self-reference, a duplicate, and an id
// pointing at nothing, so the handler tests covering those still exercise them
// now that the assembly lives behind the interface.
func (m *mockStore) Retrieve(_ context.Context, id string) (store.RetrieveResult, error) {
	mem, ok := m.memories[id]
	if !ok {
		return store.RetrieveResult{}, fmt.Errorf("memory %q not found", id)
	}
	linked := make([]store.SearchResult, 0, len(mem.LinkedIDs))
	seen := make(map[string]bool, len(mem.LinkedIDs))
	for _, linkedID := range mem.LinkedIDs {
		if linkedID == id || seen[linkedID] {
			continue
		}
		seen[linkedID] = true
		peer, ok := m.memories[linkedID]
		if !ok {
			continue
		}
		linked = append(linked, store.SearchResult{
			ID:        peer.ID,
			Title:     peer.Title,
			Tags:      peer.Tags,
			CreatedAt: peer.CreatedAt,
			UpdatedAt: peer.UpdatedAt,
		})
	}
	return store.RetrieveResult{
		ID:        mem.ID,
		Title:     mem.Title,
		Content:   mem.Content,
		Tags:      mem.Tags,
		CreatedAt: mem.CreatedAt,
		UpdatedAt: mem.UpdatedAt,
		Linked:    linked,
	}, nil
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
	if m.updateErr != nil {
		// Mirrors the real store: a rejected patch leaves the record untouched.
		return m.updateErr
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
	if len(patch.AddTags) > 0 {
		mem.Tags = dedupeStringsForTest(append(append([]string{}, mem.Tags...), patch.AddTags...))
	}
	if len(patch.RemoveTags) > 0 {
		remove := make(map[string]bool, len(patch.RemoveTags))
		for _, t := range patch.RemoveTags {
			remove[t] = true
		}
		kept := mem.Tags[:0:0]
		for _, t := range mem.Tags {
			if !remove[t] {
				kept = append(kept, t)
			}
		}
		mem.Tags = kept
	}
	if patch.LinkedIDs != nil {
		mem.LinkedIDs = *patch.LinkedIDs
	}
	if len(patch.AddLinkedIDs) > 0 {
		mem.LinkedIDs = dedupeStringsForTest(append(append([]string{}, mem.LinkedIDs...), patch.AddLinkedIDs...))
	}
	if len(patch.RemoveLinkedIDs) > 0 {
		remove := make(map[string]bool, len(patch.RemoveLinkedIDs))
		for _, t := range patch.RemoveLinkedIDs {
			remove[t] = true
		}
		kept := mem.LinkedIDs[:0:0]
		for _, t := range mem.LinkedIDs {
			if !remove[t] {
				kept = append(kept, t)
			}
		}
		mem.LinkedIDs = kept
	}
	m.memories[id] = mem
	return nil
}

// captureSearchArgsStore wraps mockStore to capture the limit passed to Search.
type captureSearchArgsStore struct {
	*mockStore
	onSearch func(limit int)
}

func (c *captureSearchArgsStore) Search(ctx context.Context, query string, tagFilter []string, limit, offset int) ([]store.SearchResult, int, error) {
	c.onSearch(limit)
	return c.mockStore.Search(ctx, query, tagFilter, limit, offset)
}

// testMaxContentChars is generous enough that content-size limits never
// interfere with tests that aren't about them; the limit tests set their own.
const testMaxContentChars = 32768

// newTestApp builds an App with realistic defaults so individual tests don't
// have to restate them.
func newTestApp(s store.Store) *App {
	return NewApp(s, 20, testMaxContentChars)
}

// makeRequest builds a CallToolRequest with the given arguments map.
func makeRequest(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	return req
}

// --- store ---

func TestHandleStoreMemory_Success(t *testing.T) {
	app := newTestApp(newMockStore())
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
	app := newTestApp(ms)

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

// A failed write's message is a contract with the caller, which for this
// server is a language model deciding whether to retry. "It failed" without
// "and nothing was written" invites it to either re-check or give up on a call
// that is perfectly safe to repeat, so these assert the wording, not just the
// error flag.

func TestHandleStoreMemory_ErrorSaysNothingWasCreated(t *testing.T) {
	ms := newMockStore()
	ms.addErr = &store.MissingLinksError{IDs: []string{"missing-a", "missing-b"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{
		"title":      "New memory",
		"content":    "new content",
		"linked_ids": []any{"missing-a", "missing-b"},
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result")
	}
	text := resultText(t, result)

	for _, want := range []string{
		"No memory was created",  // the fact the caller needs to retry safely
		"nothing was written",    // and that no peer record moved either
		"missing-a", "missing-b", // every bad id, so one retry can fix them all
		"linked_ids", // which argument to fix
	} {
		if !strings.Contains(text, want) {
			t.Errorf("error message is missing %q: %s", want, text)
		}
	}
}

func TestHandleUpdateMemory_ErrorSaysTheMemoryWasNotModified(t *testing.T) {
	ms := newMockStore()
	id, err := ms.Add(context.Background(), "Existing", "content", nil, nil)
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	ms.updateErr = &store.MissingLinksError{IDs: []string{"missing"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{
		"memory_id":      id,
		"add_linked_ids": []any{"missing"},
	})

	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result")
	}
	text := resultText(t, result)

	for _, want := range []string{"The memory was not modified", "nothing was written", "missing", "linked_ids"} {
		if !strings.Contains(text, want) {
			t.Errorf("error message is missing %q: %s", want, text)
		}
	}
}

func TestHandleStoreMemory_NonLinkErrorStillSaysNothingWasWritten(t *testing.T) {
	// The all-or-nothing statement holds for every failure, not just link
	// validation: an embedding failure leaves nothing behind either.
	ms := newMockStore()
	ms.addErr = fmt.Errorf("embedding chunk 0: backend unavailable")
	app := newTestApp(ms)

	result, err := app.handleStoreMemory(context.Background(), makeRequest(map[string]any{
		"title":   "New memory",
		"content": "new content",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "No memory was created") || !strings.Contains(text, "safe to retry") {
		t.Errorf("a non-link failure should still say nothing was written: %s", text)
	}
	if strings.Contains(text, "linked_ids") {
		t.Errorf("a failure unrelated to links should not blame linked_ids: %s", text)
	}
}

func TestHandleStoreMemory_FailedStoreCreatesNothing(t *testing.T) {
	ms := newMockStore()
	ms.addErr = fmt.Errorf("linked memory %q not found", "missing")
	app := newTestApp(ms)

	req := makeRequest(map[string]any{
		"title":      "New memory",
		"content":    "new content",
		"linked_ids": []any{"missing"},
	})

	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "not found") {
		t.Errorf("error should explain what went wrong, got: %s", text)
	}
	// Add commits the record and its links together, so a rejected link leaves
	// nothing behind. Reporting an id here would send the caller looking for a
	// memory that does not exist.
	if strings.Contains(text, "test-id-") {
		t.Errorf("error names a memory id, but a failed store creates none: %s", text)
	}
	if len(ms.memories) != 0 {
		t.Errorf("store holds %d memories after a failed add, want none", len(ms.memories))
	}
}

func TestHandleStoreMemory_TitleRequired(t *testing.T) {
	app := newTestApp(newMockStore())
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
	app := newTestApp(newMockStore())
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
	app := newTestApp(newMockStore())
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
	app := newTestApp(newMockStore())
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
	app := newTestApp(newMockStore())
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
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"query": "standing desk"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Error("expected at least one result")
	}
}

func TestHandleSearchMemory_ResultShapeOnlyIDTitleTags(t *testing.T) {
	ms := newMockStore()
	ms.memories["x"] = store.Memory{ID: "x", Title: "Title", Content: "some content here", Tags: []string{"a"}}
	app := newTestApp(ms)

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
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"tag_filter": []any{"kubernetes"}})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != "a" {
		t.Errorf("expected 1 kubernetes memory, got %d", len(resp.Results))
	}
}

func TestHandleSearchMemory_TagFilterIsCaseInsensitive(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "Kubernetes fact", Content: "kubernetes fact", Tags: []string{"Kubernetes", "infra"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"tag_filter": []any{"KUBERNETES"}})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != "a" {
		t.Errorf("expected a case-insensitive tag_filter to still match, got %d results", len(resp.Results))
	}
}

func TestHandleSearchMemory_NoArgsListsEverything(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "content a"}
	ms.memories["b"] = store.Memory{ID: "b", Title: "B", Content: "content b"}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected omitting both query and tag_filter to list everything, got error: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(resp.Results) != 2 || resp.Total != 2 {
		t.Errorf("expected both memories listed with total 2, got %d results, total %d", len(resp.Results), resp.Total)
	}
}

func TestHandleSearchMemory_TagFilterOnly_StaleArgumentIgnored(t *testing.T) {
	// min_score was removed from the tool schema. A client still sending it
	// (an old cached schema, a stale prompt) must be tolerated, not rejected.
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "Tagged", Content: "content", Tags: []string{"x"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"tag_filter": []any{"x"}, "min_score": 0.9})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected a removed argument to be harmlessly ignored, got error: %v", result.Content)
	}
}

func TestHandleSearchMemory_ReturnsResults(t *testing.T) {
	ms := newMockStore()
	for i := 0; i < 10; i++ {
		ms.memories[fmt.Sprintf("id-%d", i)] = store.Memory{
			ID: fmt.Sprintf("id-%d", i), Title: "Item", Content: "item",
		}
	}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"query": "item"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var resp searchMemoryResponse
	json.Unmarshal([]byte(resultText(t, result)), &resp) //nolint:errcheck
	if len(resp.Results) != 10 {
		t.Errorf("expected 10 results, got %d", len(resp.Results))
	}
}

func TestHandleSearchMemory_OffsetAndTotal(t *testing.T) {
	ms := newMockStore()
	for i := 0; i < 5; i++ {
		ms.memories[fmt.Sprintf("id-%d", i)] = store.Memory{ID: fmt.Sprintf("id-%d", i), Title: "Item", Content: "item"}
	}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"query": "item", "limit": 2, "offset": 3})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("expected total 5, got %d", resp.Total)
	}
	if len(resp.Results) != 2 {
		t.Errorf("expected 2 results after skipping 3 of 5, got %d", len(resp.Results))
	}
}

func TestHandleSearchMemory_LimitDefault(t *testing.T) {
	var capturedLimit int
	ms := newMockStore()
	ms.memories["id-1"] = store.Memory{ID: "id-1", Title: "Item", Content: "item"}

	app := NewApp(&captureSearchArgsStore{mockStore: ms, onSearch: func(l int) { capturedLimit = l }}, 20, testMaxContentChars)
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

	app := NewApp(&captureSearchArgsStore{mockStore: ms, onSearch: func(l int) { capturedLimit = l }}, 20, testMaxContentChars)
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

	app := NewApp(&captureSearchArgsStore{mockStore: ms, onSearch: func(l int) { capturedLimit = l }}, 20, testMaxContentChars)
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
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"query": "anything"})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result when store returns error")
	}
}

func TestHandleSearchMemory_TagFilterMultipleTagsIsAND(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "Both tags", Content: "x", Tags: []string{"kubernetes", "infra"}}
	ms.memories["b"] = store.Memory{ID: "b", Title: "One tag", Content: "x", Tags: []string{"kubernetes"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"tag_filter": []any{"kubernetes", "infra"}})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != "a" {
		t.Errorf("expected multiple tag_filter values to AND together, got %+v", resp.Results)
	}
}

func TestHandleSearchMemory_TagFilterExactNotSubstring(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "Entity note", Content: "x", Tags: []string{"entity"}}
	ms.memories["b"] = store.Memory{ID: "b", Title: "Identity note", Content: "x", Tags: []string{"workload-identity"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"tag_filter": []any{"entity"}})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != "a" {
		t.Errorf("expected tag_filter to match exactly, not as a substring of workload-identity, got %+v", resp.Results)
	}
}

func TestHandleSearchMemory_TagFilterEmptyArrayMatchesEverything(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "content a", Tags: []string{"x"}}
	ms.memories["b"] = store.Memory{ID: "b", Title: "B", Content: "content b"}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"tag_filter": []any{}})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected an explicit empty tag_filter to be accepted, got error: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(resp.Results) != 2 || resp.Total != 2 {
		t.Errorf("expected an explicit empty tag_filter to behave like omitting it, got %d results, total %d", len(resp.Results), resp.Total)
	}
}

func TestHandleSearchMemory_TagFilterMultipleTagsWithQuery(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "Both tags", Content: "cluster admin notes", Tags: []string{"kubernetes", "infra"}}
	ms.memories["b"] = store.Memory{ID: "b", Title: "One tag", Content: "cluster admin notes", Tags: []string{"kubernetes"}}
	ms.memories["c"] = store.Memory{ID: "c", Title: "Both tags, other content", Content: "unrelated", Tags: []string{"kubernetes", "infra"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"query": "cluster admin notes", "tag_filter": []any{"kubernetes", "infra"}})
	result, err := app.handleSearchMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var resp searchMemoryResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("parsing results: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != "a" {
		t.Errorf("expected a query plus multiple ANDed tag_filter values to narrow to one result, got %+v", resp.Results)
	}
}

// --- list_tags ---

func TestHandleListTags_ReturnsSortedDistinctTags(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Tags: []string{"kubernetes", "infra"}}
	ms.memories["b"] = store.Memory{ID: "b", Title: "B", Tags: []string{"golang", "infra"}}
	app := newTestApp(ms)

	result, err := app.handleListTags(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var tags []string
	if err := json.Unmarshal([]byte(resultText(t, result)), &tags); err != nil {
		t.Fatalf("parsing tags: %v", err)
	}
	want := []string{"golang", "infra", "kubernetes"}
	if len(tags) != len(want) {
		t.Fatalf("expected %v, got %v", want, tags)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Errorf("expected %v, got %v", want, tags)
			break
		}
	}
}

func TestHandleListTags_EmptyStore(t *testing.T) {
	app := newTestApp(newMockStore())

	result, err := app.handleListTags(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var tags []string
	if err := json.Unmarshal([]byte(resultText(t, result)), &tags); err != nil {
		t.Fatalf("parsing tags: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected no tags for an empty store, got %v", tags)
	}
}

// --- retrieve ---

func TestHandleRetrieveMemory_Success(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "content a", Tags: []string{"t1"}, CreatedAt: "2026-01-01T00:00:00Z"}
	app := newTestApp(ms)

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
	app := newTestApp(ms)

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
	app := newTestApp(ms)

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

func TestHandleRetrieveMemory_FiltersSelfAndDuplicateLinks(t *testing.T) {
	// A corrupted/hand-edited DB (e.g. via CSV import, which writes
	// linked_ids verbatim without normalizeLinks) could contain a
	// self-reference or a duplicate id; retrieve must not surface either.
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "content a", LinkedIDs: []string{"a", "b", "b"}}
	ms.memories["b"] = store.Memory{ID: "b", Title: "B", Content: "content b"}
	app := newTestApp(ms)

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
	if len(got.Linked) != 1 || got.Linked[0].ID != "b" {
		t.Errorf("expected exactly one linked entry (b), with self-reference and duplicate dropped, got %v", got.Linked)
	}
}

func TestHandleRetrieveMemory_NotFound(t *testing.T) {
	app := newTestApp(newMockStore())
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
	app := newTestApp(newMockStore())
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
	app := newTestApp(ms)

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
	app := newTestApp(newMockStore())
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
	app := newTestApp(newMockStore())
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
	app := newTestApp(ms)

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
	app := newTestApp(ms)

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
	app := newTestApp(ms)

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

func TestHandleUpdateMemory_AddTags(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content", Tags: []string{"kept"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "upd-id", "add_tags": []any{"new"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	got := ms.memories["upd-id"].Tags
	if len(got) != 2 || got[0] != "kept" || got[1] != "new" {
		t.Errorf("expected add_tags to union with the existing set, got %v", got)
	}
}

func TestHandleUpdateMemory_RemoveTags(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content", Tags: []string{"keep", "drop"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "upd-id", "remove_tags": []any{"drop"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	got := ms.memories["upd-id"].Tags
	if len(got) != 1 || got[0] != "keep" {
		t.Errorf("expected remove_tags to drop only the listed tag, got %v", got)
	}
}

func TestHandleUpdateMemory_AddAndRemoveTagsTogether(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content", Tags: []string{"old"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "upd-id", "add_tags": []any{"new"}, "remove_tags": []any{"old"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	got := ms.memories["upd-id"].Tags
	if len(got) != 1 || got[0] != "new" {
		t.Errorf("expected add_tags and remove_tags combined to swap the tag, got %v", got)
	}
}

func TestHandleUpdateMemory_AddTagsAlreadyPresentIsNoop(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content", Tags: []string{"existing"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "upd-id", "add_tags": []any{"existing"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	got := ms.memories["upd-id"].Tags
	if len(got) != 1 || got[0] != "existing" {
		t.Errorf("expected adding an already-present tag to be a no-op, got %v", got)
	}
}

func TestHandleUpdateMemory_TagsRejectsCombinationWithAddTags(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content", Tags: []string{"old"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "upd-id", "tags": []any{"new"}, "add_tags": []any{"extra"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected combining tags with add_tags to be rejected")
	}
}

func TestHandleUpdateMemory_TagsRejectsCombinationWithRemoveTags(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content", Tags: []string{"old"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "upd-id", "tags": []any{"new"}, "remove_tags": []any{"old"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected combining tags with remove_tags to be rejected")
	}
}

func TestHandleUpdateMemory_AddTagsAloneSatisfiesAtLeastOneField(t *testing.T) {
	ms := newMockStore()
	ms.memories["upd-id"] = store.Memory{ID: "upd-id", Title: "Title", Content: "content"}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "upd-id", "add_tags": []any{"new"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected add_tags alone to satisfy the at-least-one-field requirement, got error: %v", result.Content)
	}
}

func TestHandleUpdateMemory_AddLinkedIDs(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "a", LinkedIDs: []string{"kept"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "a", "add_linked_ids": []any{"new"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	got := ms.memories["a"].LinkedIDs
	if len(got) != 2 || got[0] != "kept" || got[1] != "new" {
		t.Errorf("expected add_linked_ids to union with the existing set, got %v", got)
	}
}

func TestHandleUpdateMemory_RemoveLinkedIDs(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "a", LinkedIDs: []string{"keep", "drop"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "a", "remove_linked_ids": []any{"drop"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	got := ms.memories["a"].LinkedIDs
	if len(got) != 1 || got[0] != "keep" {
		t.Errorf("expected remove_linked_ids to drop only the listed id, got %v", got)
	}
}

func TestHandleUpdateMemory_AddAndRemoveLinkedIDsTogether(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "a", LinkedIDs: []string{"old"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "a", "add_linked_ids": []any{"new"}, "remove_linked_ids": []any{"old"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	got := ms.memories["a"].LinkedIDs
	if len(got) != 1 || got[0] != "new" {
		t.Errorf("expected add_linked_ids and remove_linked_ids combined to swap the link, got %v", got)
	}
}

func TestHandleUpdateMemory_LinkedIDsRejectsCombinationWithAddLinkedIDs(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "a", LinkedIDs: []string{"old"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "a", "linked_ids": []any{"new"}, "add_linked_ids": []any{"extra"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected combining linked_ids with add_linked_ids to be rejected")
	}
}

func TestHandleUpdateMemory_LinkedIDsRejectsCombinationWithRemoveLinkedIDs(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "a", LinkedIDs: []string{"old"}}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "a", "linked_ids": []any{"new"}, "remove_linked_ids": []any{"old"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected combining linked_ids with remove_linked_ids to be rejected")
	}
}

func TestHandleUpdateMemory_AddLinkedIDsAloneSatisfiesAtLeastOneField(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "a"}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "a", "add_linked_ids": []any{"new"}})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected add_linked_ids alone to satisfy the at-least-one-field requirement, got error: %v", result.Content)
	}
}

func TestHandleUpdateMemory_LinkedIDsOnlyPatch(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "A", Content: "a"}
	ms.memories["b"] = store.Memory{ID: "b", Title: "B", Content: "b"}
	app := newTestApp(ms)

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
	app := newTestApp(ms)

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
	app := newTestApp(ms)

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
	app := newTestApp(ms)

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
	app := newTestApp(newMockStore())
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
	app := newTestApp(newMockStore())
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

func TestHandleStoreMemory_ContentSizeLimit(t *testing.T) {
	app := NewApp(newMockStore(), 20, 100)

	ok := makeRequest(map[string]any{"title": "T", "content": strings.Repeat("a", 100)})
	result, err := app.handleStoreMemory(context.Background(), ok)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Errorf("content exactly at the limit should be accepted, got: %v", result.Content)
	}

	over := makeRequest(map[string]any{"title": "T", "content": strings.Repeat("a", 101)})
	result, err = app.handleStoreMemory(context.Background(), over)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected content over the limit to be rejected")
	}
	if msg := resultText(t, result); !strings.Contains(msg, "max_content_chars") {
		t.Errorf("expected the error to name the config knob so it is actionable, got: %s", msg)
	}
}

func TestHandleStoreMemory_ContentLimitCountsRunesNotBytes(t *testing.T) {
	// A multi-byte character must count as one, matching how the title cap
	// behaves — otherwise a limit of 100 would reject ~34 emoji.
	app := NewApp(newMockStore(), 20, 100)

	req := makeRequest(map[string]any{"title": "T", "content": strings.Repeat("é", 100)})
	result, err := app.handleStoreMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Errorf("100 multi-byte characters should fit a 100 character limit, got: %v", result.Content)
	}
}

func TestHandleUpdateMemory_ContentSizeLimit(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "T", Content: "original"}
	app := NewApp(ms, 20, 100)

	req := makeRequest(map[string]any{"memory_id": "a", "content": strings.Repeat("a", 101)})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected update to enforce the same content limit as store")
	}
	if ms.memories["a"].Content != "original" {
		t.Error("a rejected update must not have modified the memory")
	}
}

func TestHandleUpdateMemory_EmptyContentStillRejected(t *testing.T) {
	ms := newMockStore()
	ms.memories["a"] = store.Memory{ID: "a", Title: "T", Content: "original"}
	app := newTestApp(ms)

	req := makeRequest(map[string]any{"memory_id": "a", "content": ""})
	result, err := app.handleUpdateMemory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected empty content to remain rejected")
	}
}
