package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

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
	return newTestStoreWithEmb(t, testEmbedFunc)
}

// newTestStoreWithEmb is newTestStore with a caller-supplied embedding
// function, for tests that need embedding to block or fail on demand.
func newTestStoreWithEmb(t *testing.T, embed EmbeddingFunc) Store {
	t.Helper()
	s, err := newBoltStoreWithEmb(
		config.Config{
			DB:    config.DBConfig{Path: t.TempDir()},
			Model: config.ModelConfig{EmbeddingModel: "test-model"},
		},
		embed,
	)
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestStore_Load_BackfillsMissingUpdatedAt proves the self-healing half of
// the updated_at rollout: a record written before the field existed still
// comes back with UpdatedAt defaulted to CreatedAt on load, without a data
// migration, the same way normalizeTags self-heals legacy mixed-case tags.
func TestStore_Load_BackfillsMissingUpdatedAt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := testNewStoreWithModel(t, dir, "test-model")

	id, err := s.Add(ctx, "Legacy", "legacy content", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Strip updated_at directly in the bolt file, bypassing the store's own
	// write path, to simulate a record written before the field existed.
	err = s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bucketMemories)
		var fields map[string]any
		if err := json.Unmarshal(mb.Get([]byte(id)), &fields); err != nil {
			return err
		}
		delete(fields, "updated_at")
		data, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		return mb.Put([]byte(id), data)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := testNewStoreWithModel(t, dir, "test-model")
	t.Cleanup(func() { _ = reopened.Close() })

	mem, err := reopened.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if mem.UpdatedAt != mem.CreatedAt {
		t.Errorf("expected legacy record's UpdatedAt backfilled to CreatedAt, got %q vs %q", mem.UpdatedAt, mem.CreatedAt)
	}
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
	if mem.UpdatedAt != mem.CreatedAt {
		t.Errorf("expected UpdatedAt to equal CreatedAt on a fresh memory, got %q vs %q", mem.UpdatedAt, mem.CreatedAt)
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

	results, _, err := s.Search(ctx, "anything", nil, 0, 0)
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

	results, _, err := s.Search(ctx, "terminal preferences", nil, 0, 0)
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

	results, _, err := s.Search(ctx, "some content", nil, 0, 0)
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

	results, _, err := s.Search(ctx, "", []string{"KUBERNETES"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result for a case-insensitive exact tag filter, got %d", len(results))
	}
}

func TestStore_Search_TagFilter_ExactNotSubstring(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Entity note", "uses entity tagging", []string{"entity"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Identity note", "uses workload identity", []string{"workload-identity"}, nil); err != nil {
		t.Fatal(err)
	}

	results, _, err := s.Search(ctx, "", []string{"entity"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Entity note" {
		t.Errorf("expected tag_filter %q to match only the exact tag, not %q as a substring, got %+v", "entity", "workload-identity", results)
	}
}

func TestStore_Search_TagFilter_MultipleTagsIsAND(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Both tags", "has both", []string{"kubernetes", "infra"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "One tag", "has one", []string{"kubernetes"}, nil); err != nil {
		t.Fatal(err)
	}

	results, _, err := s.Search(ctx, "", []string{"kubernetes", "infra"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Both tags" {
		t.Errorf("expected multiple tag_filter values to AND together, got %+v", results)
	}
}

func TestStore_Search_Query_TagFilter_ExactNotSubstring(t *testing.T) {
	// A ranked query filters in denseScoresLocked and sparseScoresLocked, not
	// in listLocked, so exactness has to be proven on that path separately.
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Entity note", "cluster admin notes", []string{"entity"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Identity note", "cluster admin notes", []string{"workload-identity"}, nil); err != nil {
		t.Fatal(err)
	}

	results, total, err := s.Search(ctx, "cluster admin notes", []string{"entity"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 1 || len(results) != 1 || results[0].Title != "Entity note" {
		t.Errorf("expected a ranked query with tag_filter %q to match only the exact tag, not %q as a substring, got total %d and %+v", "entity", "workload-identity", total, results)
	}
}

func TestStore_Search_Query_TagFilter_MultipleTagsIsAND(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Both tags", "cluster admin notes", []string{"kubernetes", "infra"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "One tag", "cluster admin notes", []string{"kubernetes"}, nil); err != nil {
		t.Fatal(err)
	}

	results, total, err := s.Search(ctx, "cluster admin notes", []string{"kubernetes", "infra"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 1 || len(results) != 1 || results[0].Title != "Both tags" {
		t.Errorf("expected multiple tag_filter values to AND together on the ranked path, got total %d and %+v", total, results)
	}
}

func TestStore_Search_TagFilter_SubstringNeitherDirectionMatches(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Fact", "a plain fact", []string{"fact"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Artifact", "a build artifact", []string{"artifact"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Factoid", "a factoid", []string{"factoid"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Refactor", "a refactor", []string{"refactor"}, nil); err != nil {
		t.Fatal(err)
	}

	results, _, err := s.Search(ctx, "", []string{"fact"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Fact" {
		t.Errorf("expected tag_filter %q to match only the tag %q, not the tags containing it, got %+v", "fact", "fact", results)
	}

	results, _, err = s.Search(ctx, "", []string{"artifact"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Artifact" {
		t.Errorf("expected tag_filter %q not to match the shorter tag %q it contains, got %+v", "artifact", "fact", results)
	}
}

func TestStore_Search_TagFilter_EmptySliceMatchesEverything(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alphaID, err := s.Add(ctx, "Alpha note", "alpha content", []string{"alpha"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "Beta note", "beta content", []string{"beta"}, nil); err != nil {
		t.Fatal(err)
	}

	results, total, err := s.Search(ctx, "", []string{}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 || total != 2 {
		t.Errorf("expected an empty tag_filter to match everything, got %d results, total %d", len(results), total)
	}

	results, _, err = s.Search(ctx, "alpha content", []string{}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.ID == alphaID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an empty tag_filter to leave a ranked query unfiltered, got %+v", results)
	}
}

func TestStore_Search_TagFilter_UnknownTagMatchesNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Kubernetes fact", "cluster admin notes", []string{"kubernetes"}, nil); err != nil {
		t.Fatal(err)
	}

	results, total, err := s.Search(ctx, "", []string{"no-such-tag"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 || total != 0 {
		t.Errorf("expected a tag_filter no memory carries to match nothing, got %d results, total %d", len(results), total)
	}

	results, total, err = s.Search(ctx, "cluster admin notes", []string{"no-such-tag"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 || total != 0 {
		t.Errorf("expected a ranked query with an unknown tag_filter to match nothing, got %d results, total %d", len(results), total)
	}
}

func TestStore_Search_TagFilter_CaseInsensitiveAcrossMultipleValues(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "Both tags", "has both", []string{"kubernetes", "infra"}, nil); err != nil {
		t.Fatal(err)
	}

	results, _, err := s.Search(ctx, "", []string{"Kubernetes", "INFRA"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Both tags" {
		t.Errorf("expected every tag_filter value to match case-insensitively, got %+v", results)
	}
}

func TestStore_Tags_ReturnsSortedDistinctTags(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "A", "a", []string{"kubernetes", "infra"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "B", "b", []string{"golang", "infra"}, nil); err != nil {
		t.Fatal(err)
	}

	tags, err := s.Tags(ctx)
	if err != nil {
		t.Fatalf("Tags: %v", err)
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

func TestStore_Tags_EmptyStore(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	tags, err := s.Tags(ctx)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected no tags for an empty store, got %v", tags)
	}
}

func TestStore_Add_NormalizesTagsToLowercase(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Mixed case tags", "content", []string{"Kubernetes", "INFRA"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kubernetes", "infra"}
	if len(mem.Tags) != len(want) {
		t.Fatalf("expected tags normalized to %v, got %v", want, mem.Tags)
	}
	for i := range want {
		if mem.Tags[i] != want[i] {
			t.Errorf("expected tags normalized to %v, got %v", want, mem.Tags)
			break
		}
	}
}

func TestStore_Update_NormalizesTagsToLowercase(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "content", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	newTags := []string{"Golang", "Systems"}
	if err := s.Update(ctx, id, MemoryUpdate{Tags: &newTags}); err != nil {
		t.Fatal(err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"golang", "systems"}
	if len(mem.Tags) != len(want) {
		t.Fatalf("expected tags normalized to %v, got %v", want, mem.Tags)
	}
	for i := range want {
		if mem.Tags[i] != want[i] {
			t.Errorf("expected tags normalized to %v, got %v", want, mem.Tags)
			break
		}
	}
}

func TestStore_Tags_CaseVariantsCollapseToOneEntry(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "A", "a", []string{"Kubernetes"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "B", "b", []string{"kubernetes"}, nil); err != nil {
		t.Fatal(err)
	}

	tags, err := s.Tags(ctx)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "kubernetes" {
		t.Errorf("expected mixed-case writes to normalize into one entry, got %v", tags)
	}

	results, _, err := s.Search(ctx, "", []string{"KUBERNETES"}, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected the normalized tag to filter to both memories, got %d results", len(results))
	}
}

// TestStore_ReadPaths_NormalizeLegacyMixedCaseTags proves the self-healing
// half of the lowercase rule: data written before normalization existed (or
// restored verbatim by CSV import / reembed) still comes back lowercased on
// every read path, without a data migration. It bypasses Add/Update to plant
// mixed-case tags directly, the way TestStore_Search_TagOnly_OrdersByCreatedAtChronologically
// bypasses them to control CreatedAt.
func TestStore_ReadPaths_NormalizeLegacyMixedCaseTags(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Legacy", "cluster admin notes", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bs := s.(*boltStore)
	mem := bs.docs[id]
	mem.Tags = []string{"Kubernetes"}
	bs.docs[id] = mem

	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "kubernetes" {
		t.Errorf("expected GetByID to normalize a legacy mixed-case tag, got %v", got.Tags)
	}

	tagOnly, _, err := s.Search(ctx, "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tagOnly) != 1 || len(tagOnly[0].Tags) != 1 || tagOnly[0].Tags[0] != "kubernetes" {
		t.Errorf("expected the tag-only Search path to normalize a legacy mixed-case tag, got %+v", tagOnly)
	}

	ranked, _, err := s.Search(ctx, "cluster admin notes", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 1 || len(ranked[0].Tags) != 1 || ranked[0].Tags[0] != "kubernetes" {
		t.Errorf("expected the ranked-query Search path to normalize a legacy mixed-case tag, got %+v", ranked)
	}

	tags, err := s.Tags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0] != "kubernetes" {
		t.Errorf("expected list_tags to normalize a legacy mixed-case tag, got %v", tags)
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

	results, _, err := s.Search(ctx, "", nil, 0, 0)
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

	results, _, err := s.Search(ctx, "", nil, 3, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results with limit, got %d", len(results))
	}
}

func TestStore_Search_TagOnly_OffsetAndTotal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 5; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Memory %d", i), "memory", nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, total, err := s.Search(ctx, "", nil, 2, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results after skipping 3 of 5, got %d", len(results))
	}

	results, total, err = s.Search(ctx, "", nil, 0, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total 5 even when offset exceeds it, got %d", total)
	}
	if len(results) != 0 {
		t.Errorf("expected no results when offset exceeds the match count, got %d", len(results))
	}
}

func TestStore_Search_Query_OffsetAndTotal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 5; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Widget %d", i), "widget content", nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	all, total, err := s.Search(ctx, "widget", nil, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != len(all) {
		t.Errorf("expected total to equal the unpaged result count %d, got %d", len(all), total)
	}
	if total == 0 {
		t.Fatal("expected at least one match for 'widget'")
	}

	paged, pagedTotal, err := s.Search(ctx, "widget", nil, 1, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if pagedTotal != total {
		t.Errorf("expected total to stay %d across the paged call, got %d", total, pagedTotal)
	}
	if len(paged) != 1 || paged[0].ID != all[1].ID {
		t.Errorf("expected offset 1, limit 1 to return the second unpaged result %q, got %+v", all[1].ID, paged)
	}
}

func TestStore_Search_TagOnly_OffsetAtTotalBoundary(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Memory %d", i), "memory", nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, total, err := s.Search(ctx, "", nil, 0, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 3 || len(results) != 1 {
		t.Errorf("expected an offset one short of total to return the last match, got %d results, total %d", len(results), total)
	}

	results, total, err = s.Search(ctx, "", nil, 0, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 3 || len(results) != 0 {
		t.Errorf("expected an offset equal to total to return nothing, got %d results, total %d", len(results), total)
	}
}

func TestStore_Search_NegativeOffsetStartsAtTheBeginning(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Widget %d", i), "widget content", nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, total, err := s.Search(ctx, "", nil, 0, -1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 3 || len(results) != 3 {
		t.Errorf("expected a negative offset to behave as 0 on the tag-only path, got %d results, total %d", len(results), total)
	}

	zero, _, err := s.Search(ctx, "widget", nil, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	negative, _, err := s.Search(ctx, "widget", nil, 0, -1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(negative) != len(zero) {
		t.Fatalf("expected a negative offset to behave as 0 on the ranked path, got %d results vs %d", len(negative), len(zero))
	}
	for i := range zero {
		if negative[i].ID != zero[i].ID {
			t.Errorf("expected offset -1 to return the same page as offset 0, got %+v vs %+v", negative, zero)
			break
		}
	}
}

func TestStore_Search_TagFilter_TotalCountsOnlyFilteredMatches(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Kept %d", i), "memory", []string{"keep"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Add(ctx, fmt.Sprintf("Dropped %d", i), "memory", []string{"drop"}, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, total, err := s.Search(ctx, "", []string{"keep"}, 2, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 3 {
		t.Errorf("expected total to count only the 3 tag_filter matches, got %d", total)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results after skipping 1 of 3 matches, got %d", len(results))
	}
	for _, r := range results {
		if !strings.HasPrefix(r.Title, "Kept") {
			t.Errorf("expected only tagged memories in the page, got %+v", r)
		}
	}
}

func TestStore_Search_TagOnly_OrdersByCreatedAtChronologically(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	idOld, err := s.Add(ctx, "Older", "older memory", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	idNew, err := s.Add(ctx, "Newer", "newer memory", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Force timestamps where a plain string sort ranks them backwards:
	// RFC3339Nano drops trailing fractional-second zeros, so a whole-second
	// stamp like "...10:00:00Z" sorts *after* "...10:00:00.3Z" as a string
	// even though it is chronologically earlier.
	bs := s.(*boltStore)
	older := bs.docs[idOld]
	older.CreatedAt = "2026-01-01T10:00:00Z"
	bs.docs[idOld] = older
	newer := bs.docs[idNew]
	newer.CreatedAt = "2026-01-01T10:00:00.3Z"
	bs.docs[idNew] = newer

	results, _, err := s.Search(ctx, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != idNew {
		t.Errorf("expected chronologically newer memory %q first, got %q", idNew, results[0].ID)
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

	results, _, err := s.Search(ctx, "cluster admin notes", []string{"kubernetes"}, 0, 0)
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

func TestStore_Update_RefreshesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "content", []string{"tag1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// Force a distinct timestamp: RFC3339Nano has nanosecond resolution, but
	// a fast test run can otherwise land Add and Update in the same tick.
	time.Sleep(time.Millisecond)

	newContent := "updated content"
	if err := s.Update(ctx, id, MemoryUpdate{Content: &newContent}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.CreatedAt != before.CreatedAt {
		t.Errorf("expected CreatedAt to stay fixed, got %q, want %q", after.CreatedAt, before.CreatedAt)
	}
	if after.UpdatedAt == before.UpdatedAt {
		t.Errorf("expected UpdatedAt to change after Update, still %q", after.UpdatedAt)
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

func TestStore_Update_AddTags(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "content", []string{"kept"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Update(ctx, id, MemoryUpdate{AddTags: []string{"New"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kept", "new"}
	if len(mem.Tags) != len(want) || mem.Tags[0] != want[0] || mem.Tags[1] != want[1] {
		t.Errorf("expected add_tags to union with the existing set (normalized to lowercase), got %v", mem.Tags)
	}
}

func TestStore_Update_RemoveTags(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "content", []string{"keep", "drop"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Update(ctx, id, MemoryUpdate{RemoveTags: []string{"DROP"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "keep" {
		t.Errorf("expected remove_tags to drop only the listed tag (case-insensitively), got %v", mem.Tags)
	}
}

func TestStore_Update_AddAndRemoveTagsTogether(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "content", []string{"old"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Update(ctx, id, MemoryUpdate{AddTags: []string{"new"}, RemoveTags: []string{"old"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "new" {
		t.Errorf("expected add_tags and remove_tags combined to swap the tag, got %v", mem.Tags)
	}
}

func TestStore_Update_AddTagsSameAsRemoveTagsEndsUpRemoved(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "content", []string{"existing"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Update(ctx, id, MemoryUpdate{AddTags: []string{"contested"}, RemoveTags: []string{"contested"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem.Tags) != 1 || mem.Tags[0] != "existing" {
		t.Errorf("expected a tag listed in both add_tags and remove_tags to end up removed, got %v", mem.Tags)
	}
}

func TestStore_Update_AddTagsWithContentAlsoTriggersReembed(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "old content", []string{"kept"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	newContent := "new content"
	if err := s.Update(ctx, id, MemoryUpdate{Content: &newContent, AddTags: []string{"new"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if mem.Content != "new content" {
		t.Errorf("expected content updated, got %q", mem.Content)
	}
	want := []string{"kept", "new"}
	if len(mem.Tags) != len(want) || mem.Tags[0] != want[0] || mem.Tags[1] != want[1] {
		t.Errorf("expected add_tags applied alongside a reembedding update, got %v", mem.Tags)
	}
}

func TestStore_Update_AddRemoveTags_NormalizesLegacyMixedCaseBase(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.Add(ctx, "Title", "content", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bs := s.(*boltStore)
	mem := bs.docs[id]
	mem.Tags = []string{"Kubernetes"}
	bs.docs[id] = mem

	if err := s.Update(ctx, id, MemoryUpdate{AddTags: []string{"kubernetes"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "kubernetes" {
		t.Errorf("expected add_tags of an already-present (but differently-cased) legacy tag not to duplicate it, got %v", got.Tags)
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

	results, _, err := s.Search(ctx, "word0 word1 word2", nil, 0, 0)
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

	results, _, err := s.Search(ctx, "word0", nil, 0, 0)
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
	results, _, err := s.Search(ctx, "word0", nil, 0, 0)
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
	results, _, err := s.Search(ctx, "word0 word1", nil, 0, 0)
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

	results, _, err := s.Search(ctx, "memory item", nil, 0, 0)
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

	results, _, err := s.Search(ctx, "memory item", nil, 3, 0)
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

	results, _, err := s.Search(ctx, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("Search after concurrent adds: %v", err)
	}
	if len(results) != goroutines {
		t.Errorf("expected %d memories after concurrent adds, got %d", goroutines, len(results))
	}
}

// blockMarker is embedded in a test's content so the test embedding function
// can single out that one call to block or fail on.
const blockMarker = "BLOCKME"

// TestStore_Update_ConcurrentTagPatchDuringReembedIsNotLost guards against a
// regression where Update's re-embed branch resolved add_tags/remove_tags
// against the snapshot it read before embedding. Embedding runs without the
// lock, so a tags-only update committing in that window was silently
// overwritten. The patch must be applied to the record as it is at commit
// time instead.
func TestStore_Update_ConcurrentTagPatchDuringReembedIsNotLost(t *testing.T) {
	ctx := context.Background()
	embedding := make(chan struct{}, 1)
	release := make(chan struct{})
	s := newTestStoreWithEmb(t, func(ctx context.Context, text string) ([]float32, error) {
		if strings.Contains(text, blockMarker) {
			select {
			case embedding <- struct{}{}:
			default:
			}
			<-release
		}
		return testEmbedFunc(ctx, text)
	})

	id, err := s.Add(ctx, "A", "original", nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newContent := "updated content " + blockMarker
	done := make(chan error, 1)
	go func() {
		done <- s.Update(ctx, id, MemoryUpdate{Content: &newContent, AddTags: []string{"x"}})
	}()

	<-embedding // the re-embed branch has taken its snapshot and is embedding
	if err := s.Update(ctx, id, MemoryUpdate{AddTags: []string{"y"}}); err != nil {
		t.Fatalf("concurrent tags-only update: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("content+add_tags update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(mem.Tags, "x") || !containsID(mem.Tags, "y") {
		t.Errorf("expected both concurrently added tags, got %v", mem.Tags)
	}
	if mem.Content != newContent {
		t.Errorf("expected the content update to apply, got %q", mem.Content)
	}
}

// TestStore_Update_EmbeddingFailureLeavesLinksUnchanged guards the ordering
// that keeps a failed update atomic from the caller's point of view: links are
// synced only after embedding succeeds, so an embedding backend outage during
// "change the content and add a link" leaves neither half applied.
func TestStore_Update_EmbeddingFailureLeavesLinksUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newTestStoreWithEmb(t, func(ctx context.Context, text string) ([]float32, error) {
		if strings.Contains(text, blockMarker) {
			return nil, fmt.Errorf("embedding backend down")
		}
		return testEmbedFunc(ctx, text)
	})

	aID, err := s.Add(ctx, "A", "a", nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	bID, err := s.Add(ctx, "B", "b", nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newContent := "updated content " + blockMarker
	if err := s.Update(ctx, aID, MemoryUpdate{Content: &newContent, AddLinkedIDs: []string{bID}}); err == nil {
		t.Fatal("expected the update to fail when embedding fails")
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.LinkedIDs) != 0 {
		t.Errorf("expected no link on A after a failed update, got %v", a.LinkedIDs)
	}
	if a.Content != "a" {
		t.Errorf("expected A's content unchanged, got %q", a.Content)
	}
	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.LinkedIDs) != 0 {
		t.Errorf("expected no back-link on B after a failed update, got %v", b.LinkedIDs)
	}
}
