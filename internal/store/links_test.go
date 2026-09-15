package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestSyncLinks_RejectedUpdateMutatesNothing is the invariant the bolt
// migration exists to guarantee. Link syncing touches several records at once;
// if validation fails partway, neither the durable store nor the in-memory
// index may retain any part of the batch. Under the old JSON sidecar this was
// upheld by a "mutate, then remember to save once" convention; here it falls
// out of the write landing in a single transaction.
func TestSyncLinks_RejectedUpdateMutatesNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cs := s.(*boltStore)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, []string{aID})
	cID, _ := s.Add(ctx, "C", "c", nil, nil)

	// Link A to C *and* to an id that doesn't exist. C is valid and is
	// processed first, so a non-atomic implementation would leave C linked to
	// A even though the operation as a whole failed.
	bad := []string{cID, "does-not-exist"}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &bad}); err == nil {
		t.Fatal("expected an error when linking to a nonexistent memory")
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(a.LinkedIDs, cID) {
		t.Errorf("A must not be linked to C after a rejected update, got %v", a.LinkedIDs)
	}
	if !containsID(a.LinkedIDs, bID) {
		t.Errorf("A's pre-existing link to B must survive a rejected update, got %v", a.LinkedIDs)
	}
	c, err := s.GetByID(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(c.LinkedIDs, aID) {
		t.Errorf("C must not have been half-linked to A, got %v", c.LinkedIDs)
	}

	// The durable copy must agree with the in-memory index.
	if stored := storedMemory(t, cs, cID); containsID(stored.LinkedIDs, aID) {
		t.Errorf("persisted C must not be linked to A, got %v", stored.LinkedIDs)
	}
	if stored := storedMemory(t, cs, aID); containsID(stored.LinkedIDs, cID) {
		t.Errorf("persisted A must not be linked to C, got %v", stored.LinkedIDs)
	}
}

// storedMemory reads a memory back out of bolt rather than the in-memory
// index, so a test can assert what actually got persisted.
func storedMemory(t *testing.T, s *boltStore, id string) Memory {
	t.Helper()
	var mem Memory
	if err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketMemories).Get([]byte(id))
		if raw == nil {
			return fmt.Errorf("no record stored for %s", id)
		}
		return json.Unmarshal(raw, &mem)
	}); err != nil {
		t.Fatalf("reading stored memory %s: %v", id, err)
	}
	return mem
}

// rawVectorBlob reads a memory's stored vector bytes straight out of bolt, so
// a test can assert whether an update re-embedded or left the vectors alone.
func rawVectorBlob(t *testing.T, s *boltStore, id string) []byte {
	t.Helper()
	var out []byte
	if err := s.db.View(func(tx *bolt.Tx) error {
		out = append([]byte(nil), tx.Bucket(bucketVectors).Get([]byte(id))...)
		return nil
	}); err != nil {
		t.Fatalf("reading vector blob for %s: %v", id, err)
	}
	if len(out) == 0 {
		t.Fatalf("no vectors stored for %s", id)
	}
	return out
}

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLinks_CreateWithLinks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, err := s.Add(ctx, "A", "content a", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bID, err := s.Add(ctx, "B", "content b", nil, []string{aID})
	if err != nil {
		t.Fatal(err)
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(a.LinkedIDs, bID) {
		t.Errorf("expected A to back-link to B, got %v", a.LinkedIDs)
	}
	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B to link to A, got %v", b.LinkedIDs)
	}
}

func TestLinks_CreateWithMultipleLinks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)
	cID, _ := s.Add(ctx, "C", "c", nil, nil)

	dID, err := s.Add(ctx, "D", "d", nil, []string{aID, bID, cID})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{aID, bID, cID} {
		mem, err := s.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !containsID(mem.LinkedIDs, dID) {
			t.Errorf("expected %s to link back to D, got %v", id, mem.LinkedIDs)
		}
	}
	d, err := s.GetByID(ctx, dID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{aID, bID, cID} {
		if !containsID(d.LinkedIDs, id) {
			t.Errorf("expected D to link to %s, got %v", id, d.LinkedIDs)
		}
	}
}

func TestLinks_UpdateAddLink(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)

	newLinks := []string{bID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &newLinks}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(a.LinkedIDs, bID) {
		t.Errorf("expected A linked to B, got %v", a.LinkedIDs)
	}
	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B linked back to A, got %v", b.LinkedIDs)
	}
}

func TestLinks_UpdateRemoveLink(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, []string{aID})

	empty := []string{}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &empty}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.LinkedIDs) != 0 {
		t.Errorf("expected A to have no links, got %v", a.LinkedIDs)
	}
	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B to no longer link to A, got %v", b.LinkedIDs)
	}
}

func TestLinks_UpdateReplaceLinks_AddAndRemoveInOneCall(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	bID, _ := s.Add(ctx, "B", "b", nil, nil)
	cID, _ := s.Add(ctx, "C", "c", nil, nil)
	aID, _ := s.Add(ctx, "A", "a", nil, []string{bID})

	newLinks := []string{cID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &newLinks}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(a.LinkedIDs, bID) || !containsID(a.LinkedIDs, cID) {
		t.Errorf("expected A linked only to C, got %v", a.LinkedIDs)
	}
	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B no longer linked to A, got %v", b.LinkedIDs)
	}
	c, err := s.GetByID(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(c.LinkedIDs, aID) {
		t.Errorf("expected C linked to A, got %v", c.LinkedIDs)
	}
}

func TestLinks_UpdateLinksOnly_DoesNotReembed(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cs := s.(*boltStore)

	aID, _ := s.Add(ctx, "A", "a content", nil, nil)
	bID, _ := s.Add(ctx, "B", "b content", nil, nil)

	before := rawVectorBlob(t, cs, aID)

	newLinks := []string{bID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &newLinks}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if after := rawVectorBlob(t, cs, aID); !bytes.Equal(before, after) {
		t.Error("expected the stored vector blob to be untouched by a links-only update")
	}
}

func TestLinks_UpdateTagsOnly_DoesNotReembed(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cs := s.(*boltStore)

	aID, _ := s.Add(ctx, "A", "a content", nil, nil)
	before := rawVectorBlob(t, cs, aID)

	newTags := []string{"fresh", "tags"}
	if err := s.Update(ctx, aID, MemoryUpdate{Tags: &newTags}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if after := rawVectorBlob(t, cs, aID); !bytes.Equal(before, after) {
		t.Error("expected the stored vector blob to be untouched by a tags-only update")
	}
}

func TestLinks_UpdateContentAlso_TriggersReembed(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "original content", nil, nil)
	bID, _ := s.Add(ctx, "B", "b content", nil, nil)

	newContent := "brand new content"
	newLinks := []string{bID}
	if err := s.Update(ctx, aID, MemoryUpdate{Content: &newContent, LinkedIDs: &newLinks}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Content != newContent {
		t.Errorf("expected content updated, got %q", a.Content)
	}
	if !containsID(a.LinkedIDs, bID) {
		t.Errorf("expected A linked to B, got %v", a.LinkedIDs)
	}
	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B linked back to A, got %v", b.LinkedIDs)
	}

	// Confirm it's actually searchable under the new content.
	results, _, err := s.Search(ctx, "brand new content", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range results {
		if r.ID == aID {
			found = true
		}
	}
	if !found {
		t.Error("expected re-embedded content to be searchable")
	}
}

func TestLinks_DeleteCascadesFromSingleLink(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, []string{aID})

	if err := s.Delete(ctx, aID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B's link to deleted A to be cleaned up, got %v", b.LinkedIDs)
	}
}

func TestLinks_DeleteCascadesFromMultipleLinks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)
	cID, _ := s.Add(ctx, "C", "c", nil, nil)
	dID, _ := s.Add(ctx, "D", "d", nil, []string{aID, bID, cID})

	if err := s.Delete(ctx, dID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for _, id := range []string{aID, bID, cID} {
		mem, err := s.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("GetByID %s: %v", id, err)
		}
		if containsID(mem.LinkedIDs, dID) {
			t.Errorf("expected %s's link to deleted D to be cleaned up, got %v", id, mem.LinkedIDs)
		}
	}
}

func TestLinks_SelfLinkIsDroppedNotErrored(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)

	selfLinks := []string{aID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &selfLinks}); err != nil {
		t.Fatalf("expected self-link to be silently dropped, got error: %v", err)
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(a.LinkedIDs, aID) {
		t.Errorf("expected self-reference stripped, got %v", a.LinkedIDs)
	}
}

func TestLinks_SelfLinkMixedWithValidLinks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)

	links := []string{aID, bID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &links}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(a.LinkedIDs, aID) {
		t.Errorf("expected self-reference stripped, got %v", a.LinkedIDs)
	}
	if !containsID(a.LinkedIDs, bID) {
		t.Errorf("expected valid link to B applied, got %v", a.LinkedIDs)
	}
}

func TestLinks_LinkToNonexistentIDErrors(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)

	bad := []string{"does-not-exist"}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &bad}); err == nil {
		t.Error("expected error linking to a nonexistent memory")
	}
}

func TestLinks_LinkToNonexistentID_MutatesNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	bID, _ := s.Add(ctx, "B", "b", nil, nil)
	aID, _ := s.Add(ctx, "A", "a", nil, []string{bID})

	before, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}

	bad := []string{"does-not-exist"}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &bad}); err == nil {
		t.Fatal("expected error linking to a nonexistent memory")
	}

	after, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStringSlices(before.LinkedIDs, after.LinkedIDs) {
		t.Errorf("expected no mutation on failed link update, before=%v after=%v", before.LinkedIDs, after.LinkedIDs)
	}
}

func TestLinks_LinkToNonexistentIDAmongValidOnes_RejectsWholeWrite(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)
	cID, _ := s.Add(ctx, "C", "c", nil, nil)

	mixed := []string{bID, "does-not-exist", cID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &mixed}); err == nil {
		t.Fatal("expected error for a mixed valid/invalid link set")
	}

	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B not to be linked since the whole write was rejected, got %v", b.LinkedIDs)
	}
	c, err := s.GetByID(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(c.LinkedIDs, aID) {
		t.Errorf("expected C not to be linked since the whole write was rejected, got %v", c.LinkedIDs)
	}
}

func TestLinks_DuplicateIDsInInputAreDeduped(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)

	dup := []string{bID, bID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &dup}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.LinkedIDs) != 1 {
		t.Errorf("expected duplicate link deduped to 1, got %v", a.LinkedIDs)
	}

	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, id := range b.LinkedIDs {
		if id == aID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected B to link back to A exactly once, got %v", b.LinkedIDs)
	}
}

func TestLinks_TriangleGraph_StaysConsistentThroughOperations(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)
	cID, _ := s.Add(ctx, "C", "c", nil, nil)

	// Form a triangle: A-B, A-C, B-C.
	linksA := []string{bID, cID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &linksA}); err != nil {
		t.Fatalf("linking A: %v", err)
	}
	linksB := []string{aID, cID}
	if err := s.Update(ctx, bID, MemoryUpdate{LinkedIDs: &linksB}); err != nil {
		t.Fatalf("linking B: %v", err)
	}

	assertLinked := func(id1, id2 string) {
		t.Helper()
		m, err := s.GetByID(ctx, id1)
		if err != nil {
			t.Fatal(err)
		}
		if !containsID(m.LinkedIDs, id2) {
			t.Errorf("expected %s linked to %s, got %v", id1, id2, m.LinkedIDs)
		}
	}
	assertNotLinked := func(id1, id2 string) {
		t.Helper()
		m, err := s.GetByID(ctx, id1)
		if err != nil {
			t.Fatal(err)
		}
		if containsID(m.LinkedIDs, id2) {
			t.Errorf("expected %s NOT linked to %s, got %v", id1, id2, m.LinkedIDs)
		}
	}

	assertLinked(aID, bID)
	assertLinked(aID, cID)
	assertLinked(bID, cID)

	// Unlink A-B by updating A's links to only C.
	onlyC := []string{cID}
	if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &onlyC}); err != nil {
		t.Fatalf("unlinking A-B: %v", err)
	}
	assertNotLinked(aID, bID)
	assertNotLinked(bID, aID)
	assertLinked(aID, cID)
	assertLinked(bID, cID)

	// Delete C: A and B should both lose their link to it, with no dangling refs.
	if err := s.Delete(ctx, cID); err != nil {
		t.Fatalf("deleting C: %v", err)
	}
	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.LinkedIDs) != 0 {
		t.Errorf("expected A to have no links left, got %v", a.LinkedIDs)
	}
	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.LinkedIDs) != 0 {
		t.Errorf("expected B to have no links left, got %v", b.LinkedIDs)
	}
}

func TestLinks_ConcurrentUpdatesOnDisjointMemories(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const pairs = 10
	ids := make([][2]string, pairs)
	for i := range ids {
		a, _ := s.Add(ctx, fmt.Sprintf("A%d", i), "a", nil, nil)
		b, _ := s.Add(ctx, fmt.Sprintf("B%d", i), "b", nil, nil)
		ids[i] = [2]string{a, b}
	}

	var wg sync.WaitGroup
	wg.Add(pairs)
	for i := 0; i < pairs; i++ {
		go func(i int) {
			defer wg.Done()
			links := []string{ids[i][1]}
			if err := s.Update(ctx, ids[i][0], MemoryUpdate{LinkedIDs: &links}); err != nil {
				t.Errorf("concurrent link %d failed: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < pairs; i++ {
		a, err := s.GetByID(ctx, ids[i][0])
		if err != nil {
			t.Fatal(err)
		}
		if !containsID(a.LinkedIDs, ids[i][1]) {
			t.Errorf("pair %d: expected link applied, got %v", i, a.LinkedIDs)
		}
	}
}

func TestLinks_ConcurrentUpdatesOnOverlappingLinks_SerializesCorrectly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)
	cID, _ := s.Add(ctx, "C", "c", nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	linksA := []string{cID}
	linksB := []string{cID}
	go func() {
		defer wg.Done()
		if err := s.Update(ctx, aID, MemoryUpdate{LinkedIDs: &linksA}); err != nil {
			t.Errorf("linking A->C: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := s.Update(ctx, bID, MemoryUpdate{LinkedIDs: &linksB}); err != nil {
			t.Errorf("linking B->C: %v", err)
		}
	}()
	wg.Wait()

	c, err := s.GetByID(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(c.LinkedIDs, aID) || !containsID(c.LinkedIDs, bID) {
		t.Errorf("expected C linked to both A and B, got %v", c.LinkedIDs)
	}
	if len(c.LinkedIDs) != 2 {
		t.Errorf("expected exactly 2 links on C (no lost update), got %v", c.LinkedIDs)
	}
}

// TestLinks_ConcurrentTagsUpdateDoesNotClobberConcurrentLink guards against a
// regression where Update's tags-only branch persisted a LinkedIDs snapshot
// taken before a concurrent Update on a peer had linked back to this memory,
// silently reverting that link.
func TestLinks_ConcurrentTagsUpdateDoesNotClobberConcurrentLink(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const pairs = 20
	aIDs := make([]string, pairs)
	bIDs := make([]string, pairs)
	for i := range aIDs {
		aIDs[i], _ = s.Add(ctx, fmt.Sprintf("A%d", i), "a", nil, nil)
		bIDs[i], _ = s.Add(ctx, fmt.Sprintf("B%d", i), "b", nil, nil)
	}

	var wg sync.WaitGroup
	wg.Add(pairs * 2)
	for i := 0; i < pairs; i++ {
		i := i
		newTags := []string{"tag1"}
		go func() {
			defer wg.Done()
			if err := s.Update(ctx, aIDs[i], MemoryUpdate{Tags: &newTags}); err != nil {
				t.Errorf("tags update on A%d: %v", i, err)
			}
		}()
		links := []string{aIDs[i]}
		go func() {
			defer wg.Done()
			if err := s.Update(ctx, bIDs[i], MemoryUpdate{LinkedIDs: &links}); err != nil {
				t.Errorf("linking B%d->A%d: %v", i, i, err)
			}
		}()
	}
	wg.Wait()

	for i := 0; i < pairs; i++ {
		a, err := s.GetByID(ctx, aIDs[i])
		if err != nil {
			t.Fatal(err)
		}
		if !containsID(a.LinkedIDs, bIDs[i]) {
			t.Errorf("pair %d: expected A linked back to B despite a concurrent tags-only update, got %v", i, a.LinkedIDs)
		}
		if !containsID(a.Tags, "tag1") {
			t.Errorf("pair %d: expected the concurrent tags update to also apply, got %v", i, a.Tags)
		}
	}
}

// TestLinks_ConcurrentContentUpdateDoesNotClobberConcurrentLink is the same
// regression as above but for Update's re-embed branch (content changed),
// which re-embeds before taking the lock. Only one pair is used (unlike the
// tags-only version above) to keep the test fast: each content update costs a
// real embedding pass, and the invariant under test is about one re-embed
// racing one link change, not about embedding throughput.
func TestLinks_ConcurrentContentUpdateDoesNotClobberConcurrentLink(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "original", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	newContent := "updated content"
	go func() {
		defer wg.Done()
		if err := s.Update(ctx, aID, MemoryUpdate{Content: &newContent}); err != nil {
			t.Errorf("content update on A: %v", err)
		}
	}()
	links := []string{aID}
	go func() {
		defer wg.Done()
		if err := s.Update(ctx, bID, MemoryUpdate{LinkedIDs: &links}); err != nil {
			t.Errorf("linking B->A: %v", err)
		}
	}()
	wg.Wait()

	a, err := s.GetByID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(a.LinkedIDs, bID) {
		t.Errorf("expected A linked back to B despite a concurrent content update, got %v", a.LinkedIDs)
	}
	if a.Content != "updated content" {
		t.Errorf("expected the concurrent content update to also apply, got %q", a.Content)
	}
}

// TestDelete_ConcurrentDoubleDeleteIsSafe guards the property the old
// cascadeUnlink no-op was defending: two racing deletes of the same id must
// not corrupt the store. Delete now holds the write lock across both the
// existence check and the transaction, so exactly one caller wins and the
// loser gets a clean not-found rather than double-unlinking peers.
func TestDelete_ConcurrentDoubleDeleteIsSafe(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	aID, _ := s.Add(ctx, "A", "a", nil, nil)
	bID, _ := s.Add(ctx, "B", "b", nil, []string{aID})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(len(errs))
	for i := range errs {
		go func(i int) {
			defer wg.Done()
			errs[i] = s.Delete(ctx, aID)
		}(i)
	}
	wg.Wait()

	var succeeded int
	for _, err := range errs {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Errorf("expected exactly one delete to succeed, got %d (errs: %v)", succeeded, errs)
	}
	if _, err := s.GetByID(ctx, aID); err == nil {
		t.Error("expected A to be gone after delete")
	}
	b, err := s.GetByID(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(b.LinkedIDs, aID) {
		t.Errorf("expected B's link to the deleted A to be cleaned up, got %v", b.LinkedIDs)
	}
}
