package store

import (
	"context"
	"strings"
	"testing"
)

func TestExportStore_WritesAllMemories(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id1, err := s.Add(ctx, "Memory one", "memory one", []string{"tag1", "tag2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Add(ctx, "Memory two", "memory two", nil, []string{id1})
	if err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	n, err := exportStore(ctx, s, &buf)
	if err != nil {
		t.Fatalf("exportStore: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 memories exported, got %d", n)
	}

	out := buf.String()
	if !strings.Contains(out, "id,title,content,tags,linked_ids,created_at,updated_at") {
		t.Errorf("expected csv header, got: %s", out)
	}
	if !strings.Contains(out, id1) || !strings.Contains(out, "tag1;tag2") {
		t.Errorf("expected memory one with joined tags, got: %s", out)
	}
	if !strings.Contains(out, id2) {
		t.Errorf("expected memory two, got: %s", out)
	}
	if !strings.Contains(out, "Memory one") || !strings.Contains(out, "Memory two") {
		t.Errorf("expected titles in export, got: %s", out)
	}
}

func TestImportStore_RejectsBadHeader(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := importStore(ctx, s, strings.NewReader("wrong,header\nfoo,bar\n"))
	if err == nil {
		t.Error("expected error for unexpected csv header")
	}
}

func TestExportImportStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newTestStore(t)

	id, err := src.Add(ctx, "Roundtrip title", "roundtrip content", []string{"a", "b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	original, err := src.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	if _, err := exportStore(ctx, src, &buf); err != nil {
		t.Fatalf("exportStore: %v", err)
	}

	dst := newTestStore(t)
	n, err := importStore(ctx, dst, strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("importStore: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 memory imported, got %d", n)
	}

	imported, err := dst.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after import: %v", err)
	}
	if imported.Title != original.Title {
		t.Errorf("title mismatch: got %q, want %q", imported.Title, original.Title)
	}
	if imported.Content != original.Content {
		t.Errorf("content mismatch: got %q, want %q", imported.Content, original.Content)
	}
	if imported.CreatedAt != original.CreatedAt {
		t.Errorf("created_at mismatch: got %q, want %q", imported.CreatedAt, original.CreatedAt)
	}
	if imported.UpdatedAt != original.UpdatedAt {
		t.Errorf("updated_at mismatch: got %q, want %q", imported.UpdatedAt, original.UpdatedAt)
	}
	if len(imported.Tags) != len(original.Tags) {
		t.Errorf("tags mismatch: got %v, want %v", imported.Tags, original.Tags)
	}
}

func TestExportImportStore_RoundTrip_PreservesLinksWithoutValidation(t *testing.T) {
	ctx := context.Background()
	src := newTestStore(t)

	aID, err := src.Add(ctx, "A", "a", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bID, err := src.Add(ctx, "B", "b", nil, []string{aID})
	if err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	if _, err := exportStore(ctx, src, &buf); err != nil {
		t.Fatalf("exportStore: %v", err)
	}

	dst := newTestStore(t)
	if _, err := importStore(ctx, dst, strings.NewReader(buf.String())); err != nil {
		t.Fatalf("importStore: %v", err)
	}

	a, err := dst.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(a.LinkedIDs, bID) {
		t.Errorf("expected A's link to B preserved, got %v", a.LinkedIDs)
	}
	b, err := dst.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B's link to A preserved, got %v", b.LinkedIDs)
	}
}

func TestImportStore_ForwardReferencedLinkDoesNotError(t *testing.T) {
	// Row 1 links to row 2's ID, which doesn't exist yet at the point row 1 is
	// imported. Import must not validate/sync links (see addMemory's doc
	// comment) — it restores linked_ids verbatim, so row order must not matter.
	ctx := context.Background()
	s := newTestStore(t)

	csv := "id,title,content,tags,linked_ids,created_at,updated_at\n" +
		"id-1,First,first content,,id-2,2024-01-01T00:00:00Z,2024-01-01T00:00:00Z\n" +
		"id-2,Second,second content,,id-1,2024-01-01T00:00:00Z,2024-01-01T00:00:00Z\n"

	n, err := importStore(ctx, s, strings.NewReader(csv))
	if err != nil {
		t.Fatalf("importStore with forward reference: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 memories imported, got %d", n)
	}

	first, err := s.GetByID(ctx, "id-1")
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(first.LinkedIDs, "id-2") {
		t.Errorf("expected id-1 to link to id-2, got %v", first.LinkedIDs)
	}
}

// TestImportStore_LegacyHeaderDefaultsUpdatedAt covers a backup taken before
// updated_at existed: the six-column form still imports, and the missing
// updated_at reads back as created_at without waiting for a reopen.
func TestImportStore_LegacyHeaderDefaultsUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	csv := "id,title,content,tags,linked_ids,created_at\n" +
		"id-1,Legacy,legacy content,tag1,,2024-01-01T00:00:00Z\n"

	n, err := importStore(ctx, s, strings.NewReader(csv))
	if err != nil {
		t.Fatalf("importStore: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 imported, got %d", n)
	}

	mem, err := s.GetByID(ctx, "id-1")
	if err != nil {
		t.Fatal(err)
	}
	if mem.CreatedAt != "2024-01-01T00:00:00Z" {
		t.Errorf("created_at mismatch: got %q", mem.CreatedAt)
	}
	if mem.UpdatedAt != mem.CreatedAt {
		t.Errorf("expected updated_at defaulted to created_at, got %q vs %q", mem.UpdatedAt, mem.CreatedAt)
	}
}

func TestImportStore_BlankIDGeneratesNewOne(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	csv := "id,title,content,tags,linked_ids,created_at,updated_at\n,Blank ID,blank id content,,,2024-01-01T00:00:00Z,2024-01-01T00:00:00Z\n"
	n, err := importStore(ctx, s, strings.NewReader(csv))
	if err != nil {
		t.Fatalf("importStore: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 memory imported, got %d", n)
	}

	results, _, err := s.Search(ctx, "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 memory in store, got %d", len(results))
	}
	if results[0].ID == "" {
		t.Error("expected a generated ID, got empty string")
	}
}
