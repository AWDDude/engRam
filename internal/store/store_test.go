package store

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	chromem "github.com/philippgille/chromem-go"

	"github.com/AWDDude/engRam/internal/config"
)

// testEmbedFunc returns a deterministic unit vector based on the input text.
// Using a small dimension (16) keeps tests fast without affecting correctness.
func testEmbedFunc(_ context.Context, text string) ([]float32, error) {
	const dim = 16
	v := make([]float32, dim)
	for i, c := range text {
		v[i%dim] += float32(c)
	}
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	if sum > 0 {
		mag := float32(math.Sqrt(float64(sum)))
		for i := range v {
			v[i] /= mag
		}
	}
	return v, nil
}

func newTestStore(t *testing.T) Store {
	t.Helper()
	s, err := newChromemStoreWithEmb(
		config.Config{
			DB:    config.DBConfig{Path: t.TempDir()},
			Model: config.ModelConfig{EmbeddingModel: "test-model"},
		},
		chromem.EmbeddingFunc(testEmbedFunc),
	)
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	return s
}

func TestStore_Add(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Go for systems work", "David uses Go for systems work", []string{"go", "work"}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty ID")
	}
}

func TestStore_AddAndGetByID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Editor preference", "prefers dark mode", []string{"ui"}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if mem.Title != "Editor preference" {
		t.Errorf("expected title 'Editor preference', got %q", mem.Title)
	}
	if mem.Content != "prefers dark mode" {
		t.Errorf("expected content 'prefers dark mode', got %q", mem.Content)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "ui" {
		t.Errorf("unexpected tags: %v", mem.Tags)
	}
	if mem.CreatedAt == "" {
		t.Error("expected non-empty CreatedAt")
	}
	if len(mem.LinkedIDs) != 0 {
		t.Errorf("expected no linked IDs, got %v", mem.LinkedIDs)
	}
}

func TestStore_Add_TitleAndContentTrustedAsGiven(t *testing.T) {
	// The store layer performs no validation (required-ness, length caps) —
	// that's enforced at the MCP handler layer. This locks in the boundary.
	ctx := context.Background()
	s := newTestStore(t)

	longTitle := strings.Repeat("x", 500)
	id, err := s.Add(ctx, "", "content with an empty and an overlong title elsewhere", nil, nil)
	if err != nil {
		t.Fatalf("expected store to accept an empty title, got error: %v", err)
	}
	if _, err := s.GetByID(ctx, id); err != nil {
		t.Fatal(err)
	}

	id2, err := s.Add(ctx, longTitle, "content", nil, nil)
	if err != nil {
		t.Fatalf("expected store to accept an overlong title, got error: %v", err)
	}
	mem, err := s.GetByID(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	if mem.Title != longTitle {
		t.Errorf("expected overlong title to be stored verbatim, got %q", mem.Title)
	}
}

func TestStore_GetByID_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.GetByID(ctx, "does-not-exist")
	if err == nil {
		t.Error("expected error for missing ID, got nil")
	}
}

func TestStore_Search_EmptyCollection(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	results, err := s.Search(ctx, "anything", "", 0, 0)
	if err != nil {
		t.Fatalf("Search on empty collection: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results on empty collection, got %d", len(results))
	}
}

func TestStore_Search_ReturnsResults(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Terminal preference", "David prefers the terminal over GUIs", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Kubernetes fact", "David is a Kubernetes administrator", nil, nil); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "terminal preferences", "", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result")
	}
	for _, r := range results {
		if r.ID == "" || r.Title == "" {
			t.Errorf("expected id and title on result, got %+v", r)
		}
	}
}

func TestStore_Search_ResultShapeOnlyHasIDTitleTags(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Some title", "some content", []string{"a"}, nil); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "some content", "", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "Some title" {
		t.Errorf("expected title 'Some title', got %q", results[0].Title)
	}
	if len(results[0].Tags) != 1 || results[0].Tags[0] != "a" {
		t.Errorf("expected tags [a], got %v", results[0].Tags)
	}
}

func TestStore_Search_TagOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Kubernetes fact", "uses kubernetes", []string{"kubernetes", "infra"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Go preference", "prefers Go", []string{"golang"}, nil); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "", "KUBE", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result for tag filter, got %d", len(results))
	}
}

func TestStore_Search_TagOnly_All(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Memory one", "memory one", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Memory two", "memory two", nil, nil); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "", "", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 memories, got %d", len(results))
	}
}

func TestStore_Search_TagOnly_Limit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 5; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Memory %d", i), "memory", nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, err := s.Search(ctx, "", "", 0, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results with limit, got %d", len(results))
	}
}

func TestStore_Search_QueryAndTagFilterCombined(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Kubernetes notes", "cluster admin notes", []string{"kubernetes"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Kubernetes other notes", "cluster admin notes", []string{"golang"}, nil); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "cluster admin notes", "kubernetes", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected query+tag_filter to narrow to 1 result, got %d", len(results))
	}
}

func TestStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "To be deleted", "to be deleted", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetByID(ctx, id); err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestStore_Delete_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Delete(ctx, "nonexistent-id"); err == nil {
		t.Error("expected error deleting nonexistent memory, got nil")
	}
}

func TestStore_Update_ContentOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Original title", "original content", []string{"tag1"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	newContent := "updated content"
	if err := s.Update(ctx, id, MemoryUpdate{Content: &newContent}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if mem.Content != newContent {
		t.Errorf("expected updated content, got %q", mem.Content)
	}
	if mem.Title != "Original title" {
		t.Errorf("title should be preserved, got %q", mem.Title)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "tag1" {
		t.Errorf("tags not preserved after update: %v", mem.Tags)
	}
}

func TestStore_Update_TitleOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Original title", "original content", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	newTitle := "New title"
	if err := s.Update(ctx, id, MemoryUpdate{Title: &newTitle}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if mem.Title != newTitle {
		t.Errorf("expected updated title, got %q", mem.Title)
	}
	if mem.Content != "original content" {
		t.Errorf("content should be preserved, got %q", mem.Content)
	}
}

func TestStore_Update_TagsOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "content", []string{"old"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	newTags := []string{"new", "tags"}
	if err := s.Update(ctx, id, MemoryUpdate{Tags: &newTags}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem.Tags) != 2 || mem.Tags[0] != "new" || mem.Tags[1] != "tags" {
		t.Errorf("expected tags replaced, got %v", mem.Tags)
	}
	if mem.Title != "Title" || mem.Content != "content" {
		t.Errorf("title/content should be preserved, got %q / %q", mem.Title, mem.Content)
	}
}

func TestStore_Update_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	newContent := "new content"
	err := s.Update(ctx, "nonexistent-id", MemoryUpdate{Content: &newContent})
	if err == nil {
		t.Error("expected error updating nonexistent memory, got nil")
	}
}

// largeContent returns a string with at least n words.
func largeContent(n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = fmt.Sprintf("word%d", i)
	}
	return strings.Join(words, " ")
}

func TestStore_Chunking_LargeContentStoredAndRetrieved(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	content := largeContent(chunkSizeWords + 50)
	id, err := s.Add(ctx, "Large content", content, []string{"large"}, nil)
	if err != nil {
		t.Fatalf("Add large content: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if mem.Content != content {
		t.Error("GetByID returned wrong content for chunked memory")
	}
}

func TestStore_Chunking_SearchFindsChunkedMemory(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	content := largeContent(chunkSizeWords + 50)
	id, err := s.Add(ctx, "Large content", content, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	results, err := s.Search(ctx, "word0 word1 word2", "", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search to find chunked memory")
	}

	found := false
	for _, r := range results {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("chunked memory ID not found in search results")
	}
}

func TestStore_Chunking_SearchDeduplicates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Content large enough to produce multiple chunks
	content := largeContent(chunkSizeWords*2 + 10)
	id, err := s.Add(ctx, "Large content", content, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	results, err := s.Search(ctx, "word0", "", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	count := 0
	for _, r := range results {
		if r.ID == id {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected chunked memory to appear exactly once in results, got %d", count)
	}
}

func TestStore_Chunking_Delete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Large content", largeContent(chunkSizeWords+50), nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetByID(ctx, id); err == nil {
		t.Error("expected error after deleting chunked memory, got nil")
	}
	// Verify chunks are gone from the vector store by confirming search returns nothing for this ID.
	results, err := s.Search(ctx, "word0", "", 0, 0)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	for _, r := range results {
		if r.ID == id {
			t.Error("deleted chunked memory still appears in search results")
		}
	}
}

func TestStore_Chunking_Update(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Store large content (chunked), then update with short content (single doc).
	id, err := s.Add(ctx, "Title", largeContent(chunkSizeWords+50), []string{"tag1"}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newContent := "short updated content"
	if err := s.Update(ctx, id, MemoryUpdate{Content: &newContent}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if mem.Content != newContent {
		t.Errorf("expected updated content %q, got %q", newContent, mem.Content)
	}
	if mem.Title != "Title" {
		t.Errorf("title not preserved after update: %q", mem.Title)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "tag1" {
		t.Errorf("tags not preserved after update: %v", mem.Tags)
	}
}

func TestStore_ChunkText(t *testing.T) {
	// Short content: single chunk.
	single := chunkText("hello world")
	if len(single) != 1 {
		t.Errorf("expected 1 chunk for short text, got %d", len(single))
	}

	// Exactly at limit: single chunk.
	atLimit := chunkText(largeContent(chunkSizeWords))
	if len(atLimit) != 1 {
		t.Errorf("expected 1 chunk at limit, got %d", len(atLimit))
	}

	// Over limit: multiple chunks.
	over := chunkText(largeContent(chunkSizeWords + 1))
	if len(over) < 2 {
		t.Errorf("expected multiple chunks over limit, got %d", len(over))
	}

	// Each chunk should be at most chunkSizeWords words.
	for i, chunk := range over {
		words := strings.Fields(chunk)
		if len(words) > chunkSizeWords {
			t.Errorf("chunk %d has %d words, exceeds chunkSizeWords=%d", i, len(words), chunkSizeWords)
		}
	}
}

func TestStore_Chunking_Update_ShortToLong(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "short content", []string{"tag1"}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newContent := largeContent(chunkSizeWords + 50)
	if err := s.Update(ctx, id, MemoryUpdate{Content: &newContent}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if mem.Content != newContent {
		t.Error("GetByID returned wrong content after short-to-long update")
	}
	if mem.Title != "Title" {
		t.Errorf("title not preserved: %q", mem.Title)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "tag1" {
		t.Errorf("tags not preserved: %v", mem.Tags)
	}

	// Verify it's searchable and deduplicated.
	results, err := s.Search(ctx, "word0 word1", "", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	count := 0
	for _, r := range results {
		if r.ID == id {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected memory to appear once in search after short-to-long update, got %d", count)
	}
}

func TestStore_Search_NoLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 8; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Memory item %d", i), fmt.Sprintf("memory item %d", i), nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, err := s.Search(ctx, "memory item", "", 0, 0)
	if err != nil {
		t.Fatalf("Search with no limit: %v", err)
	}
	if len(results) != 8 {
		t.Errorf("expected all 8 results with no limit, got %d", len(results))
	}
}

func TestStore_Search_QueryLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 8; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Memory item %d", i), fmt.Sprintf("memory item %d", i), nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, err := s.Search(ctx, "memory item", "", 0, 3)
	if err != nil {
		t.Fatalf("Search with limit: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results with limit, got %d", len(results))
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			if _, err := s.Add(ctx, fmt.Sprintf("Concurrent memory %d", i), fmt.Sprintf("concurrent memory %d", i), nil, nil); err != nil {
				t.Errorf("concurrent Add %d failed: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	results, err := s.Search(ctx, "", "", 0, 0)
	if err != nil {
		t.Fatalf("Search after concurrent adds: %v", err)
	}
	if len(results) != goroutines {
		t.Errorf("expected %d memories after concurrent adds, got %d", goroutines, len(results))
	}
}
