package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// These tests exist because the daemon turned concurrent writes from a
// theoretical case into the normal one: every Claude session now shares a
// single store, so two of them updating, linking and deleting at the same
// moment is routine rather than a curiosity.

// assertLinksConsistent sweeps the whole store and fails if any link is
// one-sided or points at a memory that is gone. This is the invariant the
// bidirectional-link design exists to hold, and the one a torn write would
// break, so it is the real assertion in every test below.
func assertLinksConsistent(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()

	results, _, err := s.Search(ctx, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("listing memories: %v", err)
	}

	links := make(map[string]map[string]bool, len(results))
	for _, r := range results {
		mem, err := s.GetByID(ctx, r.ID)
		if err != nil {
			t.Fatalf("GetByID %s: %v", r.ID, err)
		}
		set := make(map[string]bool, len(mem.LinkedIDs))
		for _, id := range mem.LinkedIDs {
			if id == mem.ID {
				t.Errorf("memory %s links to itself", mem.ID)
			}
			if set[id] {
				t.Errorf("memory %s lists link %s twice", mem.ID, id)
			}
			set[id] = true
		}
		links[mem.ID] = set
	}

	for id, peers := range links {
		for peer := range peers {
			back, ok := links[peer]
			if !ok {
				t.Errorf("memory %s links to %s, which no longer exists", id, peer)
				continue
			}
			if !back[id] {
				t.Errorf("memory %s links to %s but %s does not link back", id, peer, peer)
			}
		}
	}
}

func TestStore_ConcurrentUpdatesKeepLinksConsistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const memories = 8
	ids := make([]string, memories)
	for i := range ids {
		id, err := s.Add(ctx, fmt.Sprintf("Memory %d", i), fmt.Sprintf("content %d", i), []string{"seed"}, nil)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		ids[i] = id
	}

	// Every pair links and unlinks from both ends at once, while other
	// goroutines rewrite the same records' text and tags. Each of those
	// touches the peer records too, so a patch that did not land in one
	// transaction would leave a link recorded on one side only.
	var wg sync.WaitGroup
	for i, id := range ids {
		for j, peer := range ids {
			if i == j {
				continue
			}
			wg.Add(1)
			go func(id, peer string) {
				defer wg.Done()
				if err := s.Update(ctx, id, MemoryUpdate{AddLinkedIDs: []string{peer}}); err != nil {
					t.Errorf("linking %s to %s: %v", id, peer, err)
				}
				if err := s.Update(ctx, id, MemoryUpdate{RemoveLinkedIDs: []string{peer}}); err != nil {
					t.Errorf("unlinking %s from %s: %v", id, peer, err)
				}
			}(id, peer)
		}

		wg.Add(2)
		go func(id string) {
			defer wg.Done()
			content := "rewritten " + id
			if err := s.Update(ctx, id, MemoryUpdate{Content: &content}); err != nil {
				t.Errorf("rewriting %s: %v", id, err)
			}
		}(id)
		go func(id string) {
			defer wg.Done()
			if err := s.Update(ctx, id, MemoryUpdate{AddTags: []string{"touched"}}); err != nil {
				t.Errorf("tagging %s: %v", id, err)
			}
		}(id)
	}
	wg.Wait()

	assertLinksConsistent(t, s)
}

func TestStore_ConcurrentDeletesLeaveNoDanglingLinks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A hub linked to every spoke, so deleting spokes concurrently makes them
	// all contend over the same record's link set.
	hub, err := s.Add(ctx, "Hub", "the hub", nil, nil)
	if err != nil {
		t.Fatalf("Add hub: %v", err)
	}
	const spokes = 12
	ids := make([]string, spokes)
	for i := range ids {
		id, err := s.Add(ctx, fmt.Sprintf("Spoke %d", i), "a spoke", nil, []string{hub})
		if err != nil {
			t.Fatalf("Add spoke: %v", err)
		}
		ids[i] = id
	}

	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			// Half are deleted and half are re-linked, so cascade cleanup and
			// link sync run against the hub at the same time.
			if i%2 == 0 {
				if err := s.Delete(ctx, id); err != nil {
					t.Errorf("deleting %s: %v", id, err)
				}
				return
			}
			if err := s.Update(ctx, id, MemoryUpdate{AddTags: []string{"kept"}}); err != nil {
				t.Errorf("tagging %s: %v", id, err)
			}
		}(i, id)
	}
	wg.Wait()

	assertLinksConsistent(t, s)

	mem, err := s.GetByID(ctx, hub)
	if err != nil {
		t.Fatalf("GetByID hub: %v", err)
	}
	if len(mem.LinkedIDs) != spokes/2 {
		t.Errorf("hub has %d links, want the %d surviving spokes", len(mem.LinkedIDs), spokes/2)
	}
}

func TestStore_UpdateAppliesLinksAndContentInOneTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	peer, err := s.Add(ctx, "Peer", "peer content", nil, nil)
	if err != nil {
		t.Fatalf("Add peer: %v", err)
	}
	id, err := s.Add(ctx, "Target", "original", nil, nil)
	if err != nil {
		t.Fatalf("Add target: %v", err)
	}

	// A patch that changes links and text together must land as one write. It
	// used to take two transactions, so another session could observe the link
	// without the new content, and a failure in between left half the patch
	// applied.
	content := "rewritten"
	if err := s.Update(ctx, id, MemoryUpdate{Content: &content, AddLinkedIDs: []string{peer}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if mem.Content != content {
		t.Errorf("content = %q, want %q", mem.Content, content)
	}
	if len(mem.LinkedIDs) != 1 || mem.LinkedIDs[0] != peer {
		t.Errorf("links = %v, want [%s]", mem.LinkedIDs, peer)
	}
	// Both halves of the patch share one timestamp because they are one write.
	if mem.UpdatedAt == mem.CreatedAt {
		t.Error("UpdatedAt was not refreshed")
	}
	assertLinksConsistent(t, s)
}

func TestStore_UpdateDoesNotWriteBackStaleTextAfterAConcurrentRewrite(t *testing.T) {
	ctx := context.Background()

	// Embedding happens outside the lock, so a title patch reads the content,
	// embeds, and only then takes the lock. Holding it inside the embed is
	// what lets a content rewrite land in exactly that window — the window
	// another session's write would find on its own once the daemon has them
	// sharing one store.
	entered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	embed := func(ctx context.Context, text string) ([]float32, error) {
		// Only the title patch embeds this text, and only its first attempt
		// waits: the retry has to be allowed through or the test deadlocks.
		if strings.Contains(text, "New title") {
			once.Do(func() {
				close(entered)
				<-gate
			})
		}
		return testEmbedFunc(ctx, text)
	}
	s := newTestStoreWithEmb(t, embed)

	id, err := s.Add(ctx, "Old title", "original content", nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	title := "New title"
	titleDone := make(chan error, 1)
	go func() { titleDone <- s.Update(ctx, id, MemoryUpdate{Title: &title}) }()

	// Wait until the title patch has read the old content and is embedding it,
	// so the rewrite below is guaranteed to land behind its back rather than
	// racing to get there first.
	<-entered
	content := "rewritten by another session"
	if err := s.Update(ctx, id, MemoryUpdate{Content: &content}); err != nil {
		t.Fatalf("content update: %v", err)
	}
	close(gate)

	if err := <-titleDone; err != nil {
		t.Fatalf("title update: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if mem.Title != title {
		t.Errorf("title = %q, want %q", mem.Title, title)
	}
	// The point of the retry: the title patch must not restore the content it
	// read before the rewrite landed.
	if mem.Content != content {
		t.Errorf("content = %q, want %q — the title update wrote back a stale snapshot", mem.Content, content)
	}
}

func TestStore_ConcurrentReadsAndWritesRaceFree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ids := make([]string, 6)
	for i := range ids {
		id, err := s.Add(ctx, fmt.Sprintf("Memory %d", i), "searchable content", []string{"seed"}, nil)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		ids[i] = id
	}

	// Readers and writers overlapping is the daemon's steady state: one
	// session searching while another stores. Run under -race, this covers the
	// read paths that serve from the in-memory maps.
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(4)
		go func(id string) {
			defer wg.Done()
			if _, _, err := s.Search(ctx, "searchable content", nil, 10, 0); err != nil {
				t.Errorf("Search: %v", err)
			}
		}(id)
		go func(id string) {
			defer wg.Done()
			if _, err := s.Retrieve(ctx, id); err != nil {
				t.Errorf("Retrieve: %v", err)
			}
		}(id)
		go func(id string) {
			defer wg.Done()
			if _, err := s.Tags(ctx); err != nil {
				t.Errorf("Tags: %v", err)
			}
		}(id)
		go func(id string) {
			defer wg.Done()
			if err := s.Update(ctx, id, MemoryUpdate{AddTags: []string{"written"}}); err != nil {
				t.Errorf("Update: %v", err)
			}
		}(id)
	}
	wg.Wait()

	for _, id := range ids {
		mem, err := s.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if !containsFold(mem.Tags, "written") {
			t.Errorf("memory %s lost its tag to a concurrent write", id)
		}
	}
}

func TestStore_RetrieveSeesLinkedMemoriesAsOneSnapshot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	peer, err := s.Add(ctx, "Peer", "peer content", []string{"Mixed"}, nil)
	if err != nil {
		t.Fatalf("Add peer: %v", err)
	}
	id, err := s.Add(ctx, "Target", "target content", nil, []string{peer})
	if err != nil {
		t.Fatalf("Add target: %v", err)
	}

	got, err := s.Retrieve(ctx, id)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got.ID != id || got.Content != "target content" {
		t.Errorf("Retrieve returned the wrong record: %+v", got)
	}
	if len(got.Linked) != 1 {
		t.Fatalf("linked = %v, want one entry", got.Linked)
	}
	if got.Linked[0].ID != peer {
		t.Errorf("linked id = %q, want %q", got.Linked[0].ID, peer)
	}
	// Retrieve is a read path, so it normalizes tags the same way the others
	// do rather than surfacing whatever case is on disk.
	if len(got.Linked[0].Tags) != 1 || got.Linked[0].Tags[0] != "mixed" {
		t.Errorf("linked tags = %v, want [mixed]", got.Linked[0].Tags)
	}
	if got.Linked[0].UpdatedAt == "" {
		t.Error("linked summary is missing updated_at")
	}
}

func TestStore_AddRejectsTheWholeWriteOnAnUnknownLink(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	good, err := s.Add(ctx, "Good", "a real memory", nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Add used to persist the record first and link afterwards, so this left
	// an unlinked memory behind and returned its id alongside the error. It
	// now lands in one transaction, matching what Update has always done with
	// an unknown link.
	id, err := s.Add(ctx, "Doomed", "links to nothing", nil, []string{good, "does-not-exist"})
	if err == nil {
		t.Fatal("expected an error linking to a memory that does not exist")
	}
	if id != "" {
		t.Errorf("Add returned id %q for a write that failed", id)
	}

	results, total, err := s.Search(ctx, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if total != 1 {
		t.Errorf("store holds %d memories, want only the one that succeeded", total)
	}
	for _, r := range results {
		if r.Title == "Doomed" {
			t.Error("the rejected memory was persisted anyway")
		}
	}

	// The valid half of the link set must not have touched its peer either.
	mem, err := s.GetByID(ctx, good)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(mem.LinkedIDs) != 0 {
		t.Errorf("peer picked up links %v from a rejected write", mem.LinkedIDs)
	}
	assertLinksConsistent(t, s)
}

func TestStore_AddLinksBothEndsInOneWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	peer, err := s.Add(ctx, "Peer", "peer content", nil, nil)
	if err != nil {
		t.Fatalf("Add peer: %v", err)
	}
	id, err := s.Add(ctx, "Linked at birth", "content", nil, []string{peer})
	if err != nil {
		t.Fatalf("Add with links: %v", err)
	}

	mem, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(mem.LinkedIDs) != 1 || mem.LinkedIDs[0] != peer {
		t.Errorf("links = %v, want [%s]", mem.LinkedIDs, peer)
	}
	assertLinksConsistent(t, s)
}

func TestStore_RetrieveReportsAMissingMemory(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Retrieve(context.Background(), "does-not-exist"); err == nil {
		t.Error("expected an error retrieving an unknown id")
	}
}
