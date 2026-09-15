package store

import (
	"context"
	"math"
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Hello, World!", []string{"hello", "world"}},
		{"withLock", []string{"withlock"}},
		{"snake_case and kebab-case", []string{"snake", "case", "and", "kebab", "case"}},
		{"CVE-2026-1234", []string{"cve", "2026", "1234"}},
		{"   ", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := tokenize(c.in)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBM25_ScoresOnlyMatchingDocs(t *testing.T) {
	ix := newBM25Index()
	ix.set("a", "Alpha", "the quick brown fox", nil)
	ix.set("b", "Beta", "lazy dogs sleeping", nil)

	scores := ix.score("fox")
	if len(scores) != 1 {
		t.Fatalf("expected only the matching doc to be scored, got %d: %v", len(scores), scores)
	}
	if _, ok := scores["a"]; !ok {
		t.Errorf("expected doc a to match 'fox', got %v", scores)
	}

	if got := ix.score("nonexistentterm"); len(got) != 0 {
		t.Errorf("expected no scores for a term absent from the corpus, got %v", got)
	}
}

func TestBM25_RarerTermScoresHigher(t *testing.T) {
	ix := newBM25Index()
	// "common" appears everywhere, "rare" only in doc a. IDF should make the
	// rare term the more discriminating signal.
	ix.set("a", "", "common rare", nil)
	ix.set("b", "", "common filler", nil)
	ix.set("c", "", "common filler", nil)
	ix.set("d", "", "common filler", nil)

	rare := ix.score("rare")["a"]
	common := ix.score("common")["a"]
	if rare <= common {
		t.Errorf("expected the rare term to outscore the ubiquitous one, rare=%f common=%f", rare, common)
	}
}

func TestBM25_TitleTermsAreBoosted(t *testing.T) {
	ix := newBM25Index()
	// Same term, same document length; only its placement differs.
	ix.set("titled", "kubernetes", "filler filler", nil)
	ix.set("bodied", "filler", "kubernetes filler", nil)

	scores := ix.score("kubernetes")
	if scores["titled"] <= scores["bodied"] {
		t.Errorf("expected a title hit to outscore a body hit, title=%f body=%f",
			scores["titled"], scores["bodied"])
	}
}

// TestBM25_TagsAreSearchable covers a real gap found against live data: a
// memory tagged "work" whose text never says "work" was unfindable by that
// word, because only title and content were indexed.
func TestBM25_TagsAreSearchable(t *testing.T) {
	ix := newBM25Index()
	ix.set("a", "David Kittle", "Sr. Cloud Engineer at 84.51 on the AI Gateway team", []string{"work", "career"})
	ix.set("b", "Grocery list", "milk eggs bread", []string{"errands"})

	scores := ix.score("work")
	if _, ok := scores["a"]; !ok {
		t.Errorf("expected a memory tagged 'work' to match the query 'work', got %v", scores)
	}
	if _, ok := scores["b"]; ok {
		t.Errorf("expected the unrelated memory not to match, got %v", scores)
	}
}

func TestKeepWithinBand(t *testing.T) {
	scores := map[string]float64{"strong": 10, "mid": 5, "weak": 1}
	got := keepWithinBand(scores, 0.4)
	if _, ok := got["weak"]; ok {
		t.Errorf("expected the weak hit to be dropped, got %v", got)
	}
	if _, ok := got["mid"]; !ok {
		t.Errorf("expected the mid hit (0.5 of best) to survive a 0.4 band, got %v", got)
	}
	if _, ok := got["strong"]; !ok {
		t.Errorf("expected the best hit to survive, got %v", got)
	}

	// An all-zero map has no best to band against and must pass through
	// untouched rather than being emptied.
	zero := map[string]float64{"a": 0, "b": 0}
	if got := keepWithinBand(zero, 0.4); len(got) != 2 {
		t.Errorf("expected all-zero scores to pass through, got %v", got)
	}
}

func TestBM25_IncrementalMatchesRebuild(t *testing.T) {
	// Drive one index through a sequence of adds, overwrites and removals, and
	// assert it ends up identical to one built fresh from the final state.
	// This is what keeps the index honest as memories are edited.
	incremental := newBM25Index()
	incremental.set("a", "Alpha", "first content about go", nil)
	incremental.set("b", "Beta", "second content about rust", nil)
	incremental.set("c", "Gamma", "third content about go", nil)
	incremental.set("b", "Beta revised", "rewritten content entirely", nil)
	incremental.remove("a")
	incremental.set("d", "Delta", "fourth content", nil)
	incremental.remove("nonexistent") // must be a no-op

	rebuilt := newBM25Index()
	rebuilt.set("b", "Beta revised", "rewritten content entirely", nil)
	rebuilt.set("c", "Gamma", "third content about go", nil)
	rebuilt.set("d", "Delta", "fourth content", nil)

	if !reflect.DeepEqual(incremental.postings, rebuilt.postings) {
		t.Errorf("postings diverged:\n incremental=%v\n rebuilt=%v", incremental.postings, rebuilt.postings)
	}
	if !reflect.DeepEqual(incremental.lengths, rebuilt.lengths) {
		t.Errorf("lengths diverged:\n incremental=%v\n rebuilt=%v", incremental.lengths, rebuilt.lengths)
	}
	if incremental.totalLen != rebuilt.totalLen {
		t.Errorf("totalLen diverged: incremental=%d rebuilt=%d", incremental.totalLen, rebuilt.totalLen)
	}
}

func TestBM25_EmptyIndexScoresNothing(t *testing.T) {
	if got := newBM25Index().score("anything"); len(got) != 0 {
		t.Errorf("expected no scores from an empty index, got %v", got)
	}
}

func TestRRF_RewardsAgreementAcrossLegs(t *testing.T) {
	// "both" is mid-ranked in each list but appears in both; "denseOnly" tops
	// one list and is absent from the other. Agreement should win.
	dense := []string{"denseOnly", "both", "x"}
	sparse := []string{"sparseOnly", "both", "y"}

	fused := rrf(dense, sparse)
	if fused["both"] <= fused["denseOnly"] {
		t.Errorf("expected a doc ranked by both legs to beat a single-leg leader, both=%f denseOnly=%f",
			fused["both"], fused["denseOnly"])
	}

	want := 1/(rrfK+2) + 1/(rrfK+2)
	if math.Abs(fused["both"]-want) > 1e-9 {
		t.Errorf("fused score for 'both' = %f, want %f", fused["both"], want)
	}
}

func TestRankedIDs_DeterministicOnTies(t *testing.T) {
	scores := map[string]float64{"b": 1.0, "a": 1.0, "c": 2.0}
	got := rankedIDs(scores)
	want := []string{"c", "a", "b"} // tie between a and b broken by id
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rankedIDs = %v, want %v", got, want)
	}
}

// TestSearch_ExactTokenBeatsDenseAlone is the case hybrid search exists for:
// a distinctive token the user half-remembers, which dense embeddings smooth
// over. The assertion has two halves — the hybrid ranks it first, and the
// dense leg alone does not — so the test fails if the lexical leg stops
// contributing rather than silently passing on the embedding's luck.
func TestSearch_ExactTokenBeatsDenseAlone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cs := s.(*boltStore)

	target, err := s.Add(ctx, "Store internals",
		"The withLock helper batches several record mutations into one save.", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, content := range []string{
		"Kubernetes cluster administration and node pools",
		"David prefers concise direct communication with bullet points",
		"Tire rotation and balance scheduled for the truck",
		"Birthday reminders for family members in June",
	} {
		if _, err := s.Add(ctx, "Unrelated "+string(rune('A'+i)), content, nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	results, _, err := s.Search(ctx, "withLock", nil, 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected the exact-token query to return the matching memory")
	}
	if results[0].ID != target {
		t.Errorf("expected the memory containing 'withLock' to rank first, got %q", results[0].Title)
	}

	// Confirm the lexical leg is what found it.
	qv, err := cs.embed(ctx, "withLock")
	if err != nil {
		t.Fatal(err)
	}
	cs.mu.RLock()
	denseRanked := rankedIDs(cs.denseScoresLocked(qv, nil))
	sparseRanked := rankedIDs(cs.sparseScoresLocked("withLock", nil))
	cs.mu.RUnlock()

	if len(sparseRanked) != 1 || sparseRanked[0] != target {
		t.Errorf("expected the lexical leg to isolate exactly the target, got %v", sparseRanked)
	}
	if len(denseRanked) > 0 && denseRanked[0] == target {
		t.Skip("this corpus's embeddings happened to rank the target first; " +
			"the hybrid assertion above still holds but this half proves nothing")
	}
}

// TestApplyRelativeCutoff exercises the filter that replaced the min_score
// knob. It is tested as a pure function on synthetic scores rather than
// through Search: the test embedder is a character-sum hash whose cosines are
// near-identical for any input, so an end-to-end assertion would measure the
// stub rather than the cutoff.
func TestApplyRelativeCutoff(t *testing.T) {
	tests := []struct {
		name   string
		scores map[string]float64
		want   []string
	}{
		{
			name: "cuts the tail below a clear winner",
			// A doc found by both legs scores ~2x a single-leg doc, which is
			// the separation this cutoff is built to act on.
			scores: map[string]float64{
				"both":   1/(rrfK+1) + 1/(rrfK+1),
				"dense1": 1 / (rrfK + 2),
				"dense2": 1 / (rrfK + 3),
			},
			want: []string{"both"},
		},
		{
			name: "keeps peers that also scored in both legs",
			scores: map[string]float64{
				"a": 1/(rrfK+1) + 1/(rrfK+1),
				"b": 1/(rrfK+2) + 1/(rrfK+2),
			},
			want: []string{"a", "b"},
		},
		{
			name: "keeps everything when nothing stands out",
			// No lexical hits: every doc sits in the narrow 1/(60+r) band, so
			// there is genuinely nothing to discriminate on.
			scores: map[string]float64{
				"a": 1 / (rrfK + 1),
				"b": 1 / (rrfK + 2),
				"c": 1 / (rrfK + 3),
			},
			want: []string{"a", "b", "c"},
		},
		{
			name:   "empty input",
			scores: map[string]float64{},
			want:   []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := applyRelativeCutoff(rankedIDs(tc.scores), tc.scores, rrfRelativeCutoff)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
