package relatedfixture

import (
	"reflect"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/recommend"
)

func TestPerformanceFixtureManifest(t *testing.T) {
	got, err := LoadManifest("../../recommend/testdata/performance/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	want := FrozenManifest()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("performance manifest = %#v, want frozen %#v", got, want)
	}
	for _, sample := range got.Samples {
		fixture, err := Generate(sample.Name, sample.Count)
		if err != nil {
			t.Fatal(err)
		}
		if digest := CorpusHash(fixture); digest != sample.SHA256 {
			t.Fatalf("sample %s-%d hash = %s, want %s", sample.Name, sample.Count, digest, sample.SHA256)
		}
	}

	mixed, err := Generate(CaseMixed, 20)
	if err != nil {
		t.Fatal(err)
	}
	languages := make(map[string]int)
	topics := make(map[string]int)
	for docID, document := range mixed.Semantics {
		if got := len(strings.Fields(document.Body)); got < 200 {
			t.Fatalf("mixed document %d tokens = %d, want at least 200", docID, got)
		}
		note := mixed.Index.Notes[document.RelPath]
		languages[note.Tags[1]]++
		topics[note.Tags[0]]++
	}
	if len(languages) != 4 || len(topics) < 5 {
		t.Fatalf("mixed balance languages/topics = %#v/%#v, want 4/5", languages, topics)
	}
}

func TestPerformanceAdversarialCases(t *testing.T) {
	fixtures := make(map[string]Fixture)
	for _, name := range []string{CaseSparsePosting, CaseTermCoverage40, CaseTermCoverage49, CaseTagCoverage49, CaseRejectedContent} {
		fixture, err := Generate(name, 100)
		if err != nil {
			t.Fatalf("Generate(%s) error = %v", name, err)
		}
		if len(fixture.Semantics) != 100 || len(fixture.Index.Notes) != 100 {
			t.Fatalf("Generate(%s) sizes = %d/%d, want 100", name, len(fixture.Semantics), len(fixture.Index.Notes))
		}
		fixtures[name] = fixture
	}
	sparse := fixtures[CaseSparsePosting]
	if got := len(strings.Fields(sparse.Semantics[0].Body)); got != 2000 {
		t.Fatalf("sparse fixture tokens = %d, want 2000", got)
	}
	sparseIndex, err := recommend.BuildFeatureIndex(sparse.Semantics, recommend.ProductionEngineParameters(5, 1).Features)
	if err != nil {
		t.Fatal(err)
	}
	if len(sparseIndex.Terms) != 100 || len(sparseIndex.Postings) != 100 {
		t.Fatalf("sparse fixture terms/postings = %d/%d, want two pair-shared terms per document", len(sparseIndex.Terms), len(sparseIndex.Postings))
	}
	for docID, document := range sparseIndex.Documents {
		if len(document.Features) != 2 {
			t.Fatalf("sparse fixture document %d features = %d, want 2", docID, len(document.Features))
		}
	}
	for termID, postings := range sparseIndex.Postings {
		if len(postings) != 2 {
			t.Fatalf("sparse fixture posting %d length = %d, want 2", termID, len(postings))
		}
	}
	boundaryMembers := 0
	for _, document := range fixtures[CaseTermCoverage40].Semantics {
		if strings.Contains(document.Body, "coverage-term-0") {
			boundaryMembers++
		}
	}
	if boundaryMembers != 40 {
		t.Fatalf("term boundary coverage members = %d, want 40", boundaryMembers)
	}
	boundaryIndex, err := recommend.BuildFeatureIndex(fixtures[CaseTermCoverage40].Semantics, recommend.ProductionEngineParameters(5, 1).Features)
	if err != nil {
		t.Fatal(err)
	}
	boundaryTerms := 0
	for termID, term := range boundaryIndex.Terms {
		if !strings.HasPrefix(term, "coverage-term-") {
			continue
		}
		boundaryTerms++
		if got := len(boundaryIndex.Postings[termID]); got != 40 {
			t.Fatalf("term boundary posting %q length = %d, want 40", term, got)
		}
	}
	if boundaryTerms != 8 {
		t.Fatalf("term boundary retained terms = %d, want 8", boundaryTerms)
	}

	termMembers := 0
	for _, document := range fixtures[CaseTermCoverage49].Semantics {
		if strings.Contains(document.Body, "coverage-term-0") {
			termMembers++
		}
	}
	if termMembers != 49 {
		t.Fatalf("term cutoff coverage members = %d, want 49", termMembers)
	}
	tagMembers := 0
	tagLanguages := make(map[string]struct{})
	for _, note := range fixtures[CaseTagCoverage49].Index.Notes {
		if len(note.Tags) >= 2 {
			tagLanguages[note.Tags[1]] = struct{}{}
		}
		if len(note.Tags) == 6 {
			tagMembers++
		}
	}
	if tagMembers != 49 || len(tagLanguages) != 4 {
		t.Fatalf("tag coverage members/languages = %d/%d, want 49/4", tagMembers, len(tagLanguages))
	}
}
