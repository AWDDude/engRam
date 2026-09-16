package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"github.com/AWDDude/engRam/internal/config"
)

var (
	bucketMemories = []byte("memories")
	bucketVectors  = []byte("vectors")
)

// boltStore implements Store on a single bbolt file.
//
// bbolt is the durable layer: every logical operation writes all of its
// records inside one transaction, which is what makes the bidirectional-link
// invariant hold by construction rather than by convention.
//
// The in-memory maps are a read cache, not a second source of truth. They are
// loaded once at open and only ever updated *after* a transaction commits, so
// a failed write leaves them untouched and the two can't diverge. Reads are
// served entirely from memory, which keeps search a tight in-process loop —
// at this corpus size brute-force cosine over every vector is microseconds,
// so no vector index is warranted.
type boltStore struct {
	db    *bolt.DB
	embed EmbeddingFunc

	mu   sync.RWMutex
	docs map[string]Memory      // memory ID -> record
	vecs map[string][][]float32 // memory ID -> one vector per content chunk
	bm25 *bm25Index             // lexical index, rebuilt at open, never persisted
}

const (
	// denseCandidateBand keeps dense hits scoring at least this fraction of
	// the best hit. Relative rather than absolute so there is no similarity
	// threshold to tune: when one memory clearly wins, the mediocre tail is
	// dropped; when everything scores alike there is nothing to distinguish
	// and all of it stays.
	denseCandidateBand = 0.75

	// sparseCandidateBand does the same for the lexical leg. It matters most
	// for queries containing a common term: "david" appears in most memories
	// and scores them all, and since RRF weighs rank rather than score, those
	// incidental hits would otherwise crowd out the memory that matched on
	// something distinctive. BM25 scores spread much wider than cosines, so
	// the band can be looser than the dense one.
	sparseCandidateBand = 0.4

	// rrfRelativeCutoff drops fused results far below the top hit. RRF scores
	// occupy a narrow band (every rank is 1/(60+r)), so the dominant signal is
	// whether a memory surfaced in both legs — roughly doubling its score.
	// At 0.5 a both-legs winner cuts the single-leg tail, while a field where
	// everything scored alike is left intact.
	//
	// This is a secondary filter: the dense leg's own band (above) does most
	// of the work, since real embeddings spread unrelated text far below a
	// genuine match.
	rrfRelativeCutoff = 0.5
)

// NewBoltStore creates a production store backed by hugot (GoMLX) for embeddings.
// The second return value is a cleanup func that must be called when the process
// exits. Returns ModelChangedError if the configured model differs from the one
// used to build the existing database — run 'engram reembed' to resolve.
func NewBoltStore(cfg config.Config) (Store, func(), error) {
	if err := os.MkdirAll(cfg.DB.Path, 0700); err != nil {
		return nil, nil, fmt.Errorf("creating db dir: %w", err)
	}

	meta, err := loadDBMeta(cfg.DB.Path)
	if err != nil {
		return nil, nil, err
	}
	if meta.ActiveModel == "" {
		if err := saveDBMeta(cfg.DB.Path, dbMeta{ActiveModel: cfg.Model.EmbeddingModel}); err != nil {
			return nil, nil, fmt.Errorf("writing db meta: %w", err)
		}
	} else if meta.ActiveModel != cfg.Model.EmbeddingModel {
		return nil, nil, &ModelChangedError{OldModel: meta.ActiveModel, NewModel: cfg.Model.EmbeddingModel}
	}

	embFn, cleanup, err := newEmbeddingFunc(context.Background(), cfg.Model.Path, cfg.Model.EmbeddingModel, cfg.Model.OnnxFilePath)
	if err != nil {
		return nil, nil, err
	}
	s, err := newBoltStoreWithEmb(cfg, embFn)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return s, func() {
		_ = s.db.Close()
		cleanup()
	}, nil
}

// newBoltStoreWithEmb creates a store with an injectable embedding function.
// Exists to allow tests to inject a mock without requiring a live model.
func newBoltStoreWithEmb(cfg config.Config, embed EmbeddingFunc) (*boltStore, error) {
	if err := os.MkdirAll(cfg.DB.Path, 0700); err != nil {
		return nil, fmt.Errorf("creating db dir: %w", err)
	}
	path := modelDBPath(cfg.DB.Path, cfg.Model.EmbeddingModel)
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening db %s: %w", path, annotateLockTimeout(err))
	}
	s := &boltStore{
		db:    db,
		embed: embed,
		docs:  make(map[string]Memory),
		vecs:  make(map[string][][]float32),
		bm25:  newBM25Index(),
	}
	if err := s.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// load creates the buckets if needed and populates the in-memory index.
func (s *boltStore) load() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		mb, err := tx.CreateBucketIfNotExists(bucketMemories)
		if err != nil {
			return fmt.Errorf("creating memories bucket: %w", err)
		}
		vb, err := tx.CreateBucketIfNotExists(bucketVectors)
		if err != nil {
			return fmt.Errorf("creating vectors bucket: %w", err)
		}
		if err := mb.ForEach(func(k, v []byte) error {
			var mem Memory
			if err := json.Unmarshal(v, &mem); err != nil {
				return fmt.Errorf("parsing memory %s: %w", k, err)
			}
			mem = backfillUpdatedAt(mem)
			s.docs[mem.ID] = mem
			s.bm25.set(mem.ID, mem.Title, mem.Content, mem.Tags)
			return nil
		}); err != nil {
			return err
		}
		return vb.ForEach(func(k, v []byte) error {
			vecs, err := decodeVectors(v)
			if err != nil {
				return fmt.Errorf("decoding vectors for %s: %w", k, err)
			}
			s.vecs[string(k)] = vecs
			return nil
		})
	})
}

// Close releases the database file lock. NewBoltStore's cleanup func calls
// this; callers that construct a store directly are responsible for it.
func (s *boltStore) Close() error {
	return s.db.Close()
}

// annotateLockTimeout turns bbolt's bare "timeout" into something actionable.
// bbolt holds an exclusive lock on the file for the lifetime of the process,
// so this almost always means another engram (typically a running MCP server)
// still has the database open.
func annotateLockTimeout(err error) error {
	if errors.Is(err, bolt.ErrTimeout) {
		return fmt.Errorf("%w: another engram process still has it open", err)
	}
	return err
}

// change is one record mutation to be applied atomically with its siblings.
type change struct {
	mem Memory
	// vectors nil leaves any stored vectors untouched, so a tags-only or
	// links-only update never rewrites (or re-embeds) the vector blob.
	vectors [][]float32
	// remove deletes the record and its vectors.
	remove bool
}

// commit writes every change in a single transaction and, only once that
// transaction has committed, applies them to the in-memory index. If the write
// fails nothing is mutated. Callers must hold s.mu for writing.
func (s *boltStore) commit(changes []change) error {
	err := s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bucketMemories)
		vb := tx.Bucket(bucketVectors)
		for _, c := range changes {
			key := []byte(c.mem.ID)
			if c.remove {
				if err := mb.Delete(key); err != nil {
					return err
				}
				if err := vb.Delete(key); err != nil {
					return err
				}
				continue
			}
			data, err := json.Marshal(c.mem)
			if err != nil {
				return err
			}
			if err := mb.Put(key, data); err != nil {
				return err
			}
			if c.vectors != nil {
				blob, err := encodeVectors(c.vectors)
				if err != nil {
					return err
				}
				if err := vb.Put(key, blob); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("writing to db: %w", err)
	}

	for _, c := range changes {
		if c.remove {
			delete(s.docs, c.mem.ID)
			delete(s.vecs, c.mem.ID)
			s.bm25.remove(c.mem.ID)
			continue
		}
		s.docs[c.mem.ID] = c.mem
		if c.vectors != nil {
			s.vecs[c.mem.ID] = c.vectors
		}
		// Re-index unconditionally: link- and tag-only changes re-index the
		// same text, which is cheap and keeps this the single place the
		// lexical index can drift from the records.
		s.bm25.set(c.mem.ID, c.mem.Title, c.mem.Content, c.mem.Tags)
	}
	return nil
}

// nowRFC3339 returns the current time as RFC3339Nano, the format Memory
// stores CreatedAt/UpdatedAt in.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func (s *boltStore) Add(ctx context.Context, title, content string, tags, linkedIDs []string) (string, error) {
	id := uuid.NewString()
	now := nowRFC3339()
	if err := s.addMemory(ctx, id, title, content, normalizeTags(tags), nil, now, now); err != nil {
		return "", err
	}
	if len(linkedIDs) > 0 {
		if _, err := s.syncLinks(id, MemoryUpdate{LinkedIDs: &linkedIDs}); err != nil {
			// The memory itself was already persisted above; only the link
			// side failed (e.g. an invalid linked ID). Return the ID so the
			// caller isn't left with an orphaned record they can't address.
			return id, fmt.Errorf("memory created but linking failed: %w", err)
		}
	}
	return id, nil
}

// addMemory stores a memory and its vectors in one transaction. Used by Add,
// Update, CSV import, and Reembed.
//
// The embedded text is "title\n\ncontent" rather than content alone, so
// semantic search matches against the title as well. Memory.Content stores raw
// content only; the combined text is embedding input, never surfaced back.
//
// linkedIDs is written verbatim with no validation or bidirectional sync —
// callers that need the link invariant enforced (Add, Update) go through
// syncLinks separately; CSV import and Reembed intentionally bypass it and
// restore linkedIDs as-is (see import.go).
func (s *boltStore) addMemory(ctx context.Context, id, title, content string, tags, linkedIDs []string, createdAt, updatedAt string) error {
	// Embedding is slow and needs no lock; do it before taking one.
	vectors, err := s.embedChunks(ctx, title, content)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// backfillUpdatedAt on the way in, not just at load: a raw add carries
	// whatever timestamps its source had, and a legacy CSV row has none. The
	// in-memory docs map serves reads directly, so defaulting here is what
	// keeps an imported legacy record reading the same before and after the
	// next reopen.
	mem := backfillUpdatedAt(Memory{
		ID:        id,
		Title:     title,
		Content:   content,
		Tags:      tags,
		LinkedIDs: linkedIDs,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	})
	return s.commit([]change{{mem: mem, vectors: vectors}})
}

// embedChunks splits "title\n\ncontent" into chunks that fit the model's token
// limit and embeds each one. Content short enough to fit yields a single vector.
func (s *boltStore) embedChunks(ctx context.Context, title, content string) ([][]float32, error) {
	chunks := chunkText(title + "\n\n" + content)
	out := make([][]float32, 0, len(chunks))
	for i, chunk := range chunks {
		v, err := s.embed(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("embedding chunk %d: %w", i, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// Search runs a hybrid query: dense vector similarity and lexical BM25 in
// parallel, fused by Reciprocal Rank Fusion. The lexical leg is what finds a
// half-remembered exact token — an identifier, an error string, a name — which
// is the case dense embeddings are worst at.
//
// query == "" switches to a tag-only scan — callers outside the MCP layer
// (export, reembed) may also pass both query and tagFilter empty to mean
// "everything". tagFilter matches by exact, case-insensitive equality; a
// memory must carry every listed tag (AND semantics). limit <= 0 means
// unlimited; offset <= 0 means from the start.
func (s *boltStore) Search(ctx context.Context, query string, tagFilter []string, limit, offset int) ([]SearchResult, int, error) {
	if query == "" {
		s.mu.RLock()
		defer s.mu.RUnlock()
		page, total := s.listLocked(tagFilter, limit, offset)
		return toSearchResults(page), total, nil
	}

	// Embedding is slow and needs no lock; do it before taking one.
	qv, err := s.embed(ctx, query)
	if err != nil {
		return nil, 0, fmt.Errorf("embedding query: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	fused := rrf(
		rankedIDs(s.denseScoresLocked(qv, tagFilter)),
		rankedIDs(s.sparseScoresLocked(query, tagFilter)),
	)

	ranked := applyRelativeCutoff(rankedIDs(fused), fused, rrfRelativeCutoff)
	total := len(ranked)
	if offset > 0 {
		if offset >= len(ranked) {
			ranked = nil
		} else {
			ranked = ranked[offset:]
		}
	}
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}

	out := make([]SearchResult, 0, len(ranked))
	for _, id := range ranked {
		mem := s.docs[id]
		out = append(out, SearchResult{ID: mem.ID, Title: mem.Title, Tags: normalizeTags(mem.Tags), CreatedAt: mem.CreatedAt, UpdatedAt: mem.UpdatedAt})
	}
	return out, total, nil
}

// denseScoresLocked scores every memory by its best-matching chunk, then keeps
// only those within denseCandidateBand of the best. Unlike the lexical leg,
// cosine similarity ranks every document in the corpus, so without this the
// dense leg would contribute a long tail of noise to the fusion.
// Callers must hold s.mu.
func (s *boltStore) denseScoresLocked(qv []float32, tagFilter []string) map[string]float64 {
	scores := make(map[string]float64, len(s.vecs))
	for id, chunks := range s.vecs {
		mem, ok := s.docs[id]
		if !ok {
			continue // orphaned vectors, skip
		}
		if !hasAllTags(mem.Tags, tagFilter) {
			continue
		}
		var top float32 = -1
		for _, cv := range chunks {
			if sc := cosine(qv, cv); sc > top {
				top = sc
			}
		}
		scores[id] = float64(top)
	}
	return keepWithinBand(scores, denseCandidateBand)
}

// sparseScoresLocked runs the BM25 query and applies the tag filter.
// Callers must hold s.mu.
func (s *boltStore) sparseScoresLocked(query string, tagFilter []string) map[string]float64 {
	scores := s.bm25.score(query)
	for id := range scores {
		mem, ok := s.docs[id]
		if !ok || !hasAllTags(mem.Tags, tagFilter) {
			delete(scores, id)
		}
	}
	return keepWithinBand(scores, sparseCandidateBand)
}

// listLocked returns a page of matching memories newest-first, plus total,
// the count of matches before offset/limit were applied. Callers must hold
// s.mu.
func (s *boltStore) listLocked(tagFilter []string, limit, offset int) (page []Memory, total int) {
	results := make([]Memory, 0, len(s.docs))
	for _, mem := range s.docs {
		if !hasAllTags(mem.Tags, tagFilter) {
			continue
		}
		results = append(results, mem)
	}
	// Sort before truncating so a limited list is deterministic and returns
	// the newest memories rather than an arbitrary subset of the map.
	//
	// CreatedAt is RFC3339Nano, which drops trailing fractional-second zeros —
	// "10:00:00Z" vs "10:00:00.3Z" — so it must be parsed rather than compared
	// as a string; a raw string compare ranks that pair, and similarly
	// ".5Z" vs ".52Z", backwards.
	sort.Slice(results, func(i, j int) bool {
		ti, erri := time.Parse(time.RFC3339Nano, results[i].CreatedAt)
		tj, errj := time.Parse(time.RFC3339Nano, results[j].CreatedAt)
		if erri == nil && errj == nil && !ti.Equal(tj) {
			return ti.After(tj)
		}
		if results[i].CreatedAt != results[j].CreatedAt {
			return results[i].CreatedAt > results[j].CreatedAt
		}
		return results[i].ID < results[j].ID
	})
	total = len(results)
	if offset > 0 {
		if offset >= len(results) {
			results = nil
		} else {
			results = results[offset:]
		}
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, total
}

// Tags returns every distinct tag currently used across stored memories,
// sorted, to aid tag_filter discoverability.
func (s *boltStore) Tags(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]bool)
	for _, mem := range s.docs {
		for _, tag := range normalizeTags(mem.Tags) {
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

func (s *boltStore) GetByID(_ context.Context, id string) (Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	mem, ok := s.docs[id]
	if !ok {
		return Memory{}, fmt.Errorf("memory %q not found", id)
	}
	mem.Tags = normalizeTags(mem.Tags)
	return mem, nil
}

// Delete removes a memory, its vectors, and every reference to it from other
// memories' LinkedIDs — all in one transaction, so no dangling reference can
// survive a partial failure.
func (s *boltStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	target, ok := s.docs[id]
	if !ok {
		return fmt.Errorf("memory %q not found", id)
	}
	changes := s.unlinkPeerChanges(id, target.LinkedIDs, nil, nowRFC3339())
	changes = append(changes, change{mem: target, remove: true})
	return s.commit(changes)
}

func (s *boltStore) Update(ctx context.Context, id string, patch MemoryUpdate) error {
	if patch.Tags != nil {
		normalized := normalizeTags(*patch.Tags)
		patch.Tags = &normalized
	}
	patch.AddTags = normalizeTags(patch.AddTags)
	patch.RemoveTags = normalizeTags(patch.RemoveTags)
	tagsChanged := patch.Tags != nil || len(patch.AddTags) > 0 || len(patch.RemoveTags) > 0
	linksChanged := patch.LinkedIDs != nil || len(patch.AddLinkedIDs) > 0 || len(patch.RemoveLinkedIDs) > 0
	needsReembed := patch.Title != nil || patch.Content != nil

	var (
		title, content string
		vectors        [][]float32
	)
	if needsReembed {
		existing, err := s.GetByID(ctx, id)
		if err != nil {
			return err
		}
		title, content = existing.Title, existing.Content
		if patch.Title != nil {
			title = *patch.Title
		}
		if patch.Content != nil {
			content = *patch.Content
		}
		// Embedding is slow and needs no lock, but it can also fail, so it
		// runs before anything is persisted: a backend outage then leaves the
		// record (and its links) exactly as it was, rather than committing
		// half the patch.
		vectors, err = s.embedChunks(ctx, title, content)
		if err != nil {
			return err
		}
	}

	// Links, if patched, are handled entirely by syncLinks, which resolves
	// LinkedIDs/AddLinkedIDs/RemoveLinkedIDs against the current set and
	// persists atomically. Everything below re-reads the current record under
	// the lock before writing, so it can't clobber that (or a concurrent link
	// change from another memory's Update) with a stale snapshot — each branch
	// overwrites only the fields it actually patched.
	if linksChanged {
		if _, err := s.syncLinks(id, patch); err != nil {
			return err
		}
	}

	if !needsReembed && !tagsChanged {
		if linksChanged {
			return nil // syncLinks above already persisted the only change.
		}
		// Nothing was patched at all; still report a not-found id.
		_, err := s.GetByID(ctx, id)
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.docs[id]
	if !ok {
		return fmt.Errorf("memory %q not found", id)
	}
	if tagsChanged {
		// Resolved against the record as it is now, not the snapshot read
		// before embedding: an add/remove patch must not silently drop a tag
		// another Update committed while this one was embedding.
		current.Tags = applyTagPatch(current.Tags, patch)
	}
	current.UpdatedAt = nowRFC3339()
	if !needsReembed {
		// vectors omitted: a tags-only change must not re-embed.
		return s.commit([]change{{mem: current}})
	}
	current.Title = title
	current.Content = content
	return s.commit([]change{{mem: current, vectors: vectors}})
}

// syncLinks resolves patch's link fields (LinkedIDs replaces the set
// outright; AddLinkedIDs/RemoveLinkedIDs adjust it incrementally, in the same
// fixed order as applyTagPatch) against id's current LinkedIDs, normalizes
// the result (self-reference and duplicates stripped), validates every
// remaining ID exists, then updates id's LinkedIDs and every peer whose
// LinkedIDs must change to keep the relationship bidirectional. Returns the
// normalized link set that was applied.
//
// The base set is read, resolved, validated, and mutated all under one lock
// and landed in one transaction, so a concurrent change to id's own link set
// can't race the read AddLinkedIDs/RemoveLinkedIDs patch against, a
// concurrent delete of a link target can't race validation, and a mid-write
// failure can't leave one side of a link updated without the other. On
// validation failure nothing is mutated.
func (s *boltStore) syncLinks(id string, patch MemoryUpdate) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	target, ok := s.docs[id]
	if !ok {
		return nil, fmt.Errorf("memory %q not found", id)
	}

	normalized := normalizeLinks(id, applyLinkPatch(target.LinkedIDs, patch))

	for _, linkID := range normalized {
		if _, ok := s.docs[linkID]; !ok {
			return nil, fmt.Errorf("linked memory %q not found", linkID)
		}
	}

	oldSet := toSet(target.LinkedIDs)
	newSet := toSet(normalized)
	now := nowRFC3339()

	var changes []change
	for _, peerID := range normalized {
		if oldSet[peerID] {
			continue // already linked, peer side already has id
		}
		peer := s.docs[peerID]
		if !toSet(peer.LinkedIDs)[id] {
			// Copy rather than append in place: the slice header in s.docs
			// may share a backing array we must not mutate before commit.
			peer.LinkedIDs = append(append([]string{}, peer.LinkedIDs...), id)
			peer.UpdatedAt = now
			changes = append(changes, change{mem: peer})
		}
	}
	changes = append(changes, s.unlinkPeerChanges(id, target.LinkedIDs, newSet, now)...)

	target.LinkedIDs = normalized
	target.UpdatedAt = now
	changes = append(changes, change{mem: target})

	if err := s.commit(changes); err != nil {
		return nil, err
	}
	return normalized, nil
}

// unlinkPeerChanges builds the changes that remove id from each peer's
// LinkedIDs, skipping any peer present in keep (nil keep skips none), and
// stamps each changed peer's UpdatedAt with now. Shared by syncLinks (which
// keeps peers still in the new link set) and Delete (which keeps none).
// Callers must hold s.mu.
func (s *boltStore) unlinkPeerChanges(id string, peerIDs []string, keep map[string]bool, now string) []change {
	var changes []change
	for _, peerID := range peerIDs {
		if keep[peerID] {
			continue
		}
		peer, ok := s.docs[peerID]
		if !ok {
			continue // peer already gone, nothing to clean up
		}
		peer.LinkedIDs = removeString(peer.LinkedIDs, id)
		peer.UpdatedAt = now
		changes = append(changes, change{mem: peer})
	}
	return changes
}

// encodeVectors serializes chunk vectors as: uint32 chunk count, uint32
// dimension, then count*dim little-endian float32s. Binary rather than JSON to
// avoid float formatting bloat and round-trip drift.
func encodeVectors(chunks [][]float32) ([]byte, error) {
	var dim int
	if len(chunks) > 0 {
		dim = len(chunks[0])
	}
	for i, c := range chunks {
		if len(c) != dim {
			return nil, fmt.Errorf("chunk %d has dimension %d, expected %d", i, len(c), dim)
		}
	}

	buf := make([]byte, 8+len(chunks)*dim*4)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(chunks)))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(dim))
	off := 8
	for _, c := range chunks {
		for _, f := range c {
			binary.LittleEndian.PutUint32(buf[off:off+4], math.Float32bits(f))
			off += 4
		}
	}
	return buf, nil
}

// maxVectorDim and maxVectorChunks bound decodeVectors' header fields before
// they're multiplied together, so a corrupt or hand-edited blob can't overflow
// the size computation into passing the length check below and then
// allocating a multi-billion-element slice. Both are far above anything the
// embedding pipeline or chunker actually produces (512-dim vectors, single- or
// low-double-digit chunk counts for any realistic memory).
const (
	maxVectorDim    = 1 << 16
	maxVectorChunks = 1 << 20
)

func decodeVectors(b []byte) ([][]float32, error) {
	if len(b) < 8 {
		return nil, fmt.Errorf("vector blob too short: %d bytes", len(b))
	}
	count := int(binary.LittleEndian.Uint32(b[0:4]))
	dim := int(binary.LittleEndian.Uint32(b[4:8]))
	if count > maxVectorChunks || dim > maxVectorDim {
		return nil, fmt.Errorf("vector blob header out of range: count=%d dim=%d", count, dim)
	}
	if want := 8 + count*dim*4; len(b) != want {
		return nil, fmt.Errorf("vector blob is %d bytes, expected %d", len(b), want)
	}

	out := make([][]float32, count)
	off := 8
	for i := range out {
		v := make([]float32, dim)
		for j := range v {
			v[j] = math.Float32frombits(binary.LittleEndian.Uint32(b[off : off+4]))
			off += 4
		}
		out[i] = v
	}
	return out, nil
}

// cosine returns the cosine similarity of two equal-length vectors, or 0 if
// they differ in length or either is a zero vector. The embedding pipeline
// normalizes its output (see embed.go), which would make this a plain dot
// product — it is computed in full anyway rather than depending on that.
func cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / float32(math.Sqrt(float64(na))*math.Sqrt(float64(nb)))
}
