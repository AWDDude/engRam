package store

import (
	"context"
	"fmt"
	"math"
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

	id, err := s.Add(ctx, "David uses Go for systems work", "fact", []string{"go", "work"})
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

	id, err := s.Add(ctx, "prefers dark mode", "preference", []string{"ui"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if mem.Content != "prefers dark mode" {
		t.Errorf("expected content 'prefers dark mode', got %q", mem.Content)
	}
	if mem.Type != "preference" {
		t.Errorf("expected type 'preference', got %q", mem.Type)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "ui" {
		t.Errorf("unexpected tags: %v", mem.Tags)
	}
	if mem.CreatedAt == "" {
		t.Error("expected non-empty CreatedAt")
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

	results, err := s.Search(ctx, "anything", 5, "")
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

	if _, err := s.Add(ctx, "David prefers the terminal over GUIs", "preference", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "David is a Kubernetes administrator", "fact", nil); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "terminal preferences", 5, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result")
	}
	for _, r := range results {
		if r.Score == 0 {
			t.Error("expected non-zero similarity score")
		}
	}
}

func TestStore_Search_TypeFilter(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "prefers vim keybindings", "preference", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "works on AI Gateway team", "fact", nil); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "preferences", 5, "preference")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		if r.Type != "preference" {
			t.Errorf("type filter not applied: got type %q", r.Type)
		}
	}
}

func TestStore_List_All(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "memory one", "fact", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "memory two", "preference", nil); err != nil {
		t.Fatal(err)
	}

	mems, err := s.List(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) != 2 {
		t.Errorf("expected 2 memories, got %d", len(mems))
	}
}

func TestStore_List_TypeFilter(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "a fact", "fact", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "a preference", "preference", nil); err != nil {
		t.Fatal(err)
	}

	mems, err := s.List(ctx, "fact", "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) != 1 {
		t.Errorf("expected 1 result, got %d", len(mems))
	}
	if mems[0].Type != "fact" {
		t.Errorf("expected type 'fact', got %q", mems[0].Type)
	}
}

func TestStore_List_TagFilter(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "uses kubernetes", "fact", []string{"kubernetes", "infra"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "prefers Go", "fact", []string{"golang"}); err != nil {
		t.Fatal(err)
	}

	mems, err := s.List(ctx, "", "KUBE", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) != 1 {
		t.Errorf("expected 1 result for tag filter, got %d", len(mems))
	}
}

func TestStore_List_Limit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 5; i++ {
		if _, err := s.Add(ctx, "memory", "fact", nil); err != nil {
			t.Fatal(err)
		}
	}

	mems, err := s.List(ctx, "", "", 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) != 3 {
		t.Errorf("expected 3 results with limit, got %d", len(mems))
	}
}

func TestStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "to be deleted", "fact", nil)
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

func TestStore_Update(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "original content", "fact", []string{"tag1"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Update(ctx, id, "updated content"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if mem.Content != "updated content" {
		t.Errorf("expected updated content, got %q", mem.Content)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "tag1" {
		t.Errorf("tags not preserved after update: %v", mem.Tags)
	}
	if mem.Type != "fact" {
		t.Errorf("type not preserved after update: %q", mem.Type)
	}
}

func TestStore_Update_NotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	err := s.Update(ctx, "nonexistent-id", "new content")
	if err == nil {
		t.Error("expected error updating nonexistent memory, got nil")
	}
}

func TestStore_Add_TaskAndActionTypes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	taskID, err := s.Add(ctx, "refactor the auth module", "task", []string{"auth"})
	if err != nil {
		t.Fatalf("Add task: %v", err)
	}
	actionID, err := s.Add(ctx, "created PR #42 fixing login bug", "action", []string{"pr", "bugfix"})
	if err != nil {
		t.Fatalf("Add action: %v", err)
	}

	task, err := s.GetByID(ctx, taskID)
	if err != nil {
		t.Fatalf("GetByID task: %v", err)
	}
	if task.Type != "task" {
		t.Errorf("expected type 'task', got %q", task.Type)
	}

	action, err := s.GetByID(ctx, actionID)
	if err != nil {
		t.Fatalf("GetByID action: %v", err)
	}
	if action.Type != "action" {
		t.Errorf("expected type 'action', got %q", action.Type)
	}
}

func TestStore_Search_NoLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 8; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("memory item %d", i), "fact", nil); err != nil {
			t.Fatal(err)
		}
	}

	results, err := s.Search(ctx, "memory item", 0, "")
	if err != nil {
		t.Fatalf("Search with no limit: %v", err)
	}
	if len(results) != 8 {
		t.Errorf("expected all 8 results with no limit, got %d", len(results))
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
			if _, err := s.Add(ctx, fmt.Sprintf("concurrent memory %d", i), "fact", nil); err != nil {
				t.Errorf("concurrent Add %d failed: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	mems, err := s.List(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("List after concurrent adds: %v", err)
	}
	if len(mems) != goroutines {
		t.Errorf("expected %d memories after concurrent adds, got %d", goroutines, len(mems))
	}
}
