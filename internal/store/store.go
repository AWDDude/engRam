package store

import (
	"context"
	"strings"
)

const (
	// chunkSizeWords is the max words per chunk (~665 tokens, comfortably
	// inside the 8192-token window of the default model). It is set well below
	// that window on purpose: a distinctive passage gets diluted in a very
	// long single embedding, so chunking still earns its place for genuinely
	// large content. At this size a typical memory embeds as one vector and is
	// never split at all.
	chunkSizeWords = 512
	// chunkOverlapWords is the number of words shared between adjacent chunks
	// to preserve context at boundaries.
	chunkOverlapWords = 64
)

// Memory is the internal representation of a stored memory.
type Memory struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	LinkedIDs []string `json:"linked_ids"`
	CreatedAt string   `json:"created_at"`
}

// SearchResult is the lightweight projection returned by Search, and reused
// for the linked-memory summaries embedded in RetrieveResult.
type SearchResult struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
}

// RetrieveResult is the full-detail response for the retrieve tool: the
// memory itself plus a one-level summary of each memory it links to.
type RetrieveResult struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Content   string         `json:"content"`
	Tags      []string       `json:"tags"`
	CreatedAt string         `json:"created_at"`
	Linked    []SearchResult `json:"linked"`
}

// MemoryUpdate is a patch: nil fields are left untouched, non-nil fields are
// applied as given (including an explicit empty slice, which clears tags or
// linked_ids).
type MemoryUpdate struct {
	Title     *string
	Content   *string
	Tags      *[]string
	LinkedIDs *[]string
}

// Store is the persistence interface for memories.
type Store interface {
	Add(ctx context.Context, title, content string, tags, linkedIDs []string) (string, error)
	// Search returns a page of matches plus total, the number of candidates
	// that matched before offset/limit were applied — for a ranked query this
	// is the count after the relevance cutoff, for query == "" (a tag-only or
	// unfiltered listing) it's the count of matching memories in the store.
	// Callers use total to tell whether they've paged through everything.
	Search(ctx context.Context, query, tagFilter string, limit, offset int) (results []SearchResult, total int, err error)
	GetByID(ctx context.Context, id string) (Memory, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id string, patch MemoryUpdate) error
}

// rawAdder is the unvalidated write path: it stores a memory with an explicit
// ID, timestamp, and linkedIDs, bypassing the bidirectional link sync that Add
// and Update enforce. CSV import and Reembed restore already-consistent data
// and deliberately need this; nothing else should use it.
type rawAdder interface {
	addMemory(ctx context.Context, id, title, content string, tags, linkedIDs []string, createdAt string) error
}

// hasMatchingTag reports whether any tag case-insensitively contains filter.
func hasMatchingTag(tags []string, filter string) bool {
	for _, tag := range tags {
		if strings.Contains(strings.ToLower(tag), strings.ToLower(filter)) {
			return true
		}
	}
	return false
}

// chunkText splits text into overlapping word-based chunks. Returns a
// single-element slice when text fits within chunkSizeWords.
func chunkText(text string) []string {
	words := strings.Fields(text)
	if len(words) <= chunkSizeWords {
		return []string{text}
	}
	stride := chunkSizeWords - chunkOverlapWords
	var chunks []string
	for start := 0; start < len(words); start += stride {
		end := start + chunkSizeWords
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[start:end], " "))
		if end == len(words) {
			break
		}
	}
	return chunks
}

func toSearchResults(mems []Memory) []SearchResult {
	out := make([]SearchResult, 0, len(mems))
	for _, mem := range mems {
		out = append(out, SearchResult{ID: mem.ID, Title: mem.Title, Tags: mem.Tags})
	}
	return out
}

// normalizeLinks dedupes ids and drops any reference to selfID, preserving
// first-seen order.
func normalizeLinks(selfID string, ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == selfID || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func toSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func removeString(ids []string, target string) []string {
	out := ids[:0:0]
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}
