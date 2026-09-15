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
//
// Tags is a full replacement; AddTags/RemoveTags are an incremental
// alternative that leaves tags not mentioned untouched. Callers should treat
// them as mutually exclusive with Tags (the MCP handler rejects combining
// them); if both somehow arrive together, Tags is applied first and
// AddTags/RemoveTags apply on top of it. AddTags and RemoveTags are plain
// slices, not pointers: nil and empty both mean "no change," since there's
// no meaningful "clear via add/remove" the way an explicit empty Tags means
// "clear all tags." A tag in both AddTags and RemoveTags ends up removed.
//
// LinkedIDs/AddLinkedIDs/RemoveLinkedIDs follow the identical pattern for
// links, resolved by syncLinks under its own lock so the base set they patch
// against can't go stale between being read and being applied.
type MemoryUpdate struct {
	Title           *string
	Content         *string
	Tags            *[]string
	AddTags         []string
	RemoveTags      []string
	LinkedIDs       *[]string
	AddLinkedIDs    []string
	RemoveLinkedIDs []string
}

// Store is the persistence interface for memories.
type Store interface {
	Add(ctx context.Context, title, content string, tags, linkedIDs []string) (string, error)
	// Search returns a page of matches plus total, the number of candidates
	// that matched before offset/limit were applied — for a ranked query this
	// is the count after the relevance cutoff, for query == "" (a tag-only or
	// unfiltered listing) it's the count of matching memories in the store.
	// Callers use total to tell whether they've paged through everything.
	//
	// tagFilter matches by exact, case-insensitive equality; a memory must
	// carry every tag listed (AND semantics) to match. An empty tagFilter
	// matches everything.
	Search(ctx context.Context, query string, tagFilter []string, limit, offset int) (results []SearchResult, total int, err error)
	GetByID(ctx context.Context, id string) (Memory, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id string, patch MemoryUpdate) error
	// Tags returns every distinct tag currently used across stored memories,
	// sorted, to aid tag_filter discoverability.
	Tags(ctx context.Context) ([]string, error)
}

// rawAdder is the unvalidated write path: it stores a memory with an explicit
// ID, timestamp, and linkedIDs, bypassing the bidirectional link sync that Add
// and Update enforce. CSV import and Reembed restore already-consistent data
// and deliberately need this; nothing else should use it.
type rawAdder interface {
	addMemory(ctx context.Context, id, title, content string, tags, linkedIDs []string, createdAt string) error
}

// hasAllTags reports whether tags contains an exact, case-insensitive match
// for every tag in filters. An empty filters matches everything. It runs once
// per memory on every filtered scan, so it compares in place rather than
// building a lookup set: both sides are a handful of tags, and allocating a
// map per memory cost more than the linear scan it saved.
func hasAllTags(tags []string, filters []string) bool {
	for _, f := range filters {
		if !containsFold(tags, f) {
			return false
		}
	}
	return true
}

func containsFold(tags []string, want string) bool {
	for _, tag := range tags {
		if strings.EqualFold(tag, want) {
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
		out = append(out, SearchResult{ID: mem.ID, Title: mem.Title, Tags: normalizeTags(mem.Tags)})
	}
	return out
}

// normalizeTags lowercases every tag so two memories can't drift into
// differently-cased "duplicate" tags, and tag_filter's exact match needs no
// per-comparison case folding. Applied on write (Add, Update) and again on
// every read path (GetByID, Search, Tags) so tags written before this rule
// existed still come back lowercased without a data migration.
func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return tags
	}
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = strings.ToLower(t)
	}
	return out
}

// applyTagPatch resolves a MemoryUpdate's tag fields against currentTags, in
// a fixed order: patch.Tags (if given) replaces the set outright, then
// patch.AddTags unions in, then patch.RemoveTags subtracts — so a tag listed
// in both AddTags and RemoveTags ends up removed. patch.Tags/AddTags/
// RemoveTags are assumed already normalized by the caller (Update does this
// once at entry); currentTags is normalized here since it may still carry
// legacy mixed-case tags read straight from storage rather than through a
// path that normalizes on read.
func applyTagPatch(currentTags []string, patch MemoryUpdate) []string {
	tags := normalizeTags(currentTags)
	if patch.Tags != nil {
		tags = *patch.Tags
	}
	if len(patch.AddTags) > 0 {
		merged := make([]string, 0, len(tags)+len(patch.AddTags))
		merged = append(merged, tags...)
		merged = append(merged, patch.AddTags...)
		tags = dedupeStrings(merged)
	}
	if len(patch.RemoveTags) > 0 {
		remove := toSet(patch.RemoveTags)
		out := tags[:0:0]
		for _, tag := range tags {
			if !remove[tag] {
				out = append(out, tag)
			}
		}
		tags = out
	}
	return tags
}

// applyLinkPatch resolves a MemoryUpdate's link fields against
// currentLinkedIDs, in the same fixed order as applyTagPatch: patch.LinkedIDs
// (if given) replaces the set outright, then AddLinkedIDs unions in, then
// RemoveLinkedIDs subtracts — so an id listed in both AddLinkedIDs and
// RemoveLinkedIDs ends up removed. The result may still contain duplicates or
// a self-reference; syncLinks runs it through normalizeLinks before use.
func applyLinkPatch(currentLinkedIDs []string, patch MemoryUpdate) []string {
	ids := currentLinkedIDs
	if patch.LinkedIDs != nil {
		ids = *patch.LinkedIDs
	}
	if len(patch.AddLinkedIDs) > 0 {
		merged := make([]string, 0, len(ids)+len(patch.AddLinkedIDs))
		merged = append(merged, ids...)
		merged = append(merged, patch.AddLinkedIDs...)
		ids = merged
	}
	if len(patch.RemoveLinkedIDs) > 0 {
		remove := toSet(patch.RemoveLinkedIDs)
		out := ids[:0:0]
		for _, i := range ids {
			if !remove[i] {
				out = append(out, i)
			}
		}
		ids = out
	}
	return ids
}

// dedupeStrings removes duplicate values, preserving first-seen order.
func dedupeStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
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
