package store

import (
	"context"
	"strings"
	"testing"
)

func TestExportStore_WritesAllMemories(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id1, err := s.Add(ctx, "memory one", "fact", []string{"tag1", "tag2"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Add(ctx, "memory two", "preference", nil)
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
	if !strings.Contains(out, "id,content,type,tags,created_at") {
		t.Errorf("expected csv header, got: %s", out)
	}
	if !strings.Contains(out, id1) || !strings.Contains(out, "tag1;tag2") {
		t.Errorf("expected memory one with joined tags, got: %s", out)
	}
	if !strings.Contains(out, id2) {
		t.Errorf("expected memory two, got: %s", out)
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

	id, err := src.Add(ctx, "roundtrip content", "task", []string{"a", "b"})
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
	if imported.Content != original.Content {
		t.Errorf("content mismatch: got %q, want %q", imported.Content, original.Content)
	}
	if imported.Type != original.Type {
		t.Errorf("type mismatch: got %q, want %q", imported.Type, original.Type)
	}
	if imported.CreatedAt != original.CreatedAt {
		t.Errorf("created_at mismatch: got %q, want %q", imported.CreatedAt, original.CreatedAt)
	}
	if len(imported.Tags) != len(original.Tags) {
		t.Errorf("tags mismatch: got %v, want %v", imported.Tags, original.Tags)
	}
}

func TestImportStore_BlankIDGeneratesNewOne(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	csv := "id,content,type,tags,created_at\n,blank id content,fact,,2024-01-01T00:00:00Z\n"
	n, err := importStore(ctx, s, strings.NewReader(csv))
	if err != nil {
		t.Fatalf("importStore: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 memory imported, got %d", n)
	}

	mems, err := s.List(ctx, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1 memory in store, got %d", len(mems))
	}
	if mems[0].ID == "" {
		t.Error("expected a generated ID, got empty string")
	}
}
