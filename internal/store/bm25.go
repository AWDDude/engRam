package store

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	bm25K1 = 1.2
	bm25B  = 0.75

	// titleBoost counts each title term this many times. Titles are short and
	// highly informative in a memory system, so a title hit should outweigh an
	// incidental mention buried in the body.
	titleBoost = 2

	// tagBoost weights tag terms the same way. Tags are deliberate, curated
	// categorization — a memory tagged "work" should be findable by that word
	// even when its text never uses it.
	tagBoost = 2

	// rrfK is the standard Reciprocal Rank Fusion constant. It damps the
	// contribution of the top ranks so one leg can't dominate the fusion.
	rrfK = 60.0
)

// tokenize lowercases and splits on any non-alphanumeric rune. Deliberately
// simple — no stemming, no stopword list — so distinctive exact tokens
// (identifiers, error strings, proper nouns) survive intact. Those are
// precisely the queries dense embeddings handle worst, and the reason this
// lexical leg exists at all.
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// bm25Index is an in-memory BM25 index maintained alongside the record index.
// Nothing is persisted: it is rebuilt from the records when the store opens,
// which at this corpus size costs microseconds.
type bm25Index struct {
	// postings maps a term to the documents containing it and their weighted
	// term counts. Scoring walks only the documents that actually match.
	postings map[string]map[string]int
	// terms maps a document to its distinct terms, so removal can find the
	// postings to clean up without scanning every term.
	terms    map[string][]string
	lengths  map[string]int
	totalLen int
	// docs holds every indexed id, including ones with zero terms (e.g. a
	// title and content that tokenize to nothing). It exists only so score's
	// corpus size for IDF counts every document, not just the ones that
	// happen to have made it into terms/lengths.
	docs map[string]struct{}
}

func newBM25Index() *bm25Index {
	return &bm25Index{
		postings: make(map[string]map[string]int),
		terms:    make(map[string][]string),
		lengths:  make(map[string]int),
		docs:     make(map[string]struct{}),
	}
}

// set indexes, or re-indexes, one memory.
func (ix *bm25Index) set(id, title, content string, tags []string) {
	ix.remove(id)
	ix.docs[id] = struct{}{}

	counts := make(map[string]int)
	for _, t := range tokenize(title) {
		counts[t] += titleBoost
	}
	for _, tag := range tags {
		for _, t := range tokenize(tag) {
			counts[t] += tagBoost
		}
	}
	for _, t := range tokenize(content) {
		counts[t]++
	}
	if len(counts) == 0 {
		return
	}

	distinct := make([]string, 0, len(counts))
	var length int
	for term, n := range counts {
		if ix.postings[term] == nil {
			ix.postings[term] = make(map[string]int)
		}
		ix.postings[term][id] = n
		distinct = append(distinct, term)
		length += n
	}

	ix.terms[id] = distinct
	ix.lengths[id] = length
	ix.totalLen += length
}

func (ix *bm25Index) remove(id string) {
	delete(ix.docs, id)
	distinct, ok := ix.terms[id]
	if !ok {
		return
	}
	for _, term := range distinct {
		delete(ix.postings[term], id)
		if len(ix.postings[term]) == 0 {
			delete(ix.postings, term)
		}
	}
	ix.totalLen -= ix.lengths[id]
	delete(ix.terms, id)
	delete(ix.lengths, id)
}

// score returns the BM25 score of every document sharing at least one term
// with the query. Documents with no overlap are absent rather than scored
// zero — that omission is what keeps the lexical leg selective.
func (ix *bm25Index) score(query string) map[string]float64 {
	scores := make(map[string]float64)
	n := len(ix.docs)
	if n == 0 {
		return scores
	}
	avgLen := float64(ix.totalLen) / float64(n)

	for _, term := range tokenize(query) {
		posting := ix.postings[term]
		df := len(posting)
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
		for id, count := range posting {
			f := float64(count)
			dl := float64(ix.lengths[id])
			scores[id] += idf * (f * (bm25K1 + 1)) / (f + bm25K1*(1-bm25B+bm25B*dl/avgLen))
		}
	}
	return scores
}

// rankedIDs orders ids by descending score, breaking ties on id so the
// ranking — and therefore the fused result — is deterministic.
func rankedIDs(scores map[string]float64) []string {
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}

// keepWithinBand drops entries scoring below band * the best score. Both
// retrieval legs narrow their candidates this way before fusion, because RRF
// sees only rank: without it, a barely-relevant hit at rank 3 contributes
// nearly as much as a strong hit at rank 2.
//
// best starts at -Inf rather than 0: cosine similarity (the dense leg) can be
// legitimately negative, and starting at 0 would silently skip the whole
// corpus down to the true, negative best score.
func keepWithinBand(scores map[string]float64, band float64) map[string]float64 {
	if len(scores) == 0 {
		return scores
	}
	best := math.Inf(-1)
	for _, score := range scores {
		if score > best {
			best = score
		}
	}
	for id, score := range scores {
		if score < best*band {
			delete(scores, id)
		}
	}
	return scores
}

// applyRelativeCutoff truncates an already-ranked list at the first entry
// scoring below relCutoff of the top entry, so one strong match doesn't drag
// along a tail of weak ones.
//
// It is deliberately relative: there is no absolute similarity threshold to
// tune. The consequence is that it only discriminates when something actually
// stands out — if every candidate scores alike, nothing is dropped, which is
// the honest outcome when the corpus offers nothing to choose between.
func applyRelativeCutoff(ranked []string, scores map[string]float64, relCutoff float64) []string {
	if len(ranked) == 0 {
		return ranked
	}
	cutoff := scores[ranked[0]] * relCutoff
	for i, id := range ranked {
		if scores[id] < cutoff {
			return ranked[:i]
		}
	}
	return ranked
}

// rrf fuses ranked lists by Reciprocal Rank Fusion: score(d) = Σ 1/(k + rank).
// Working on ranks rather than raw scores sidesteps the fact that BM25 scores
// and cosine similarities live on completely incompatible scales, so no
// normalization or per-leg weighting is needed.
func rrf(lists ...[]string) map[string]float64 {
	fused := make(map[string]float64)
	for _, list := range lists {
		for i, id := range list {
			fused[id] += 1 / (rrfK + float64(i+1))
		}
	}
	return fused
}
