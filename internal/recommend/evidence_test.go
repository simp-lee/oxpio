package recommend

import (
	"math"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestTopicShapeTable(t *testing.T) {
	tests := []struct {
		term string
		want bool
	}{
		{term: "数据库", want: true},
		{term: "ab", want: true},
		{term: "ab12", want: true},
		{term: "c++", want: true},
		{term: "C#", want: true},
		{term: "face", want: true},
		{term: "abcdefabcdefabc", want: true},
		{term: strings.Repeat("z", 48), want: true},
		{term: "a", want: false},
		{term: "中", want: false},
		{term: "中1", want: false},
		{term: "ab123", want: false},
		{term: "12345", want: false},
		{term: "2026-04-06", want: false},
		{term: "2026/4/6", want: false},
		{term: "2026.04.6", want: false},
		{term: "v2", want: false},
		{term: "VERSION2.1-3", want: false},
		{term: "VERSION١.٢", want: false},
		{term: "550e8400-e29b-41d4-a716-446655440000", want: false},
		{term: "deadbeefcafebabe", want: false},
		{term: "dead:beef/cafe_babe", want: false},
		{term: strings.Repeat("a", 49), want: false},
	}
	for _, test := range tests {
		if got := TopicShape(test.term); got != test.want {
			t.Errorf("TopicShape(%q) = %v, want %v", test.term, got, test.want)
		}
	}
}

func TestTopicEvidence(t *testing.T) {
	for _, fields := range []fieldMask{fieldTitle, fieldAliases, fieldHeadings, fieldTitle | fieldBody} {
		if !topicEvidence("database", Feature{Fields: fields}) {
			t.Errorf("topicEvidence(database, fields=%04b) = false, want true", fields)
		}
	}
	if topicEvidence("database", Feature{Fields: fieldBody}) {
		t.Fatal("topicEvidence(database, body only) = true, want false")
	}
	if topicEvidence("2026-04-06", Feature{Fields: fieldTitle}) {
		t.Fatal("topicEvidence(date, title) = true, want false")
	}
}

func TestContentEvidenceGate(t *testing.T) {
	parameters := ContentParameters{MinCosine: 0.05, MaxSingleTermRatio: 0.10}
	twoTerms := evidenceIndex(
		[]string{"database", "protocol"},
		[]int{2, 2},
		[]Feature{{TermID: 0, Weight: 0.8, Fields: fieldBody}, {TermID: 1, Weight: 0.6, Fields: fieldBody}},
		[]Feature{{TermID: 0, Weight: 0.8, Fields: fieldBody}, {TermID: 1, Weight: 0.6, Fields: fieldBody}},
	)
	if got, err := qualifiedContentScore(twoTerms, 0, 1, parameters); err != nil || got != 1 {
		t.Fatalf("two-term content score = %v, %v; want 1, nil", got, err)
	}

	mixedShape := evidenceIndex(
		[]string{"database", "v2"},
		[]int{2, 2},
		[]Feature{{TermID: 0, Weight: 0.8, Fields: fieldBody}, {TermID: 1, Weight: 0.6, Fields: fieldBody}},
		[]Feature{{TermID: 0, Weight: 0.8, Fields: fieldBody}, {TermID: 1, Weight: 0.6, Fields: fieldBody}},
	)
	if got, err := qualifiedContentScore(mixedShape, 0, 1, parameters); err != nil || got != 1 {
		t.Fatalf("topic-plus-weak two-term score = %v, %v; want 1, nil", got, err)
	}

	single := evidenceIndex(
		[]string{"database"},
		[]int{2},
		[]Feature{{TermID: 0, Weight: 1, Fields: fieldTitle}},
		[]Feature{{TermID: 0, Weight: 1, Fields: fieldHeadings}},
	)
	if got, err := qualifiedContentScore(single, 0, 1, parameters); err != nil || got != 1 {
		t.Fatalf("single rare evidenced content score = %v, %v; want 1, nil", got, err)
	}
	single.Documents[1].Features[0].Fields = fieldBody
	if got, err := qualifiedContentScore(single, 0, 1, parameters); err != nil || got != 0 {
		t.Fatalf("single body-only content score = %v, %v; want 0, nil", got, err)
	}

	threeDocs := evidenceIndex(
		[]string{"database"},
		[]int{3},
		[]Feature{{TermID: 0, Weight: 1, Fields: fieldTitle}},
		[]Feature{{TermID: 0, Weight: 1, Fields: fieldTitle}},
		[]Feature{{TermID: 0, Weight: 1, Fields: fieldTitle}},
	)
	if got, err := qualifiedContentScore(threeDocs, 0, 1, parameters); err != nil || got != 0 {
		t.Fatalf("small-corpus DF=3 single-term score = %v, %v; want 0, nil", got, err)
	}

	large := evidenceIndexWithDocumentCount(20, []string{"database"}, []int{2})
	large.Documents[0].Features = []Feature{{TermID: 0, Weight: 1, Fields: fieldTitle}}
	large.Documents[1].Features = []Feature{{TermID: 0, Weight: 1, Fields: fieldTitle}}
	if got, err := qualifiedContentScore(large, 0, 1, parameters); err != nil || got != 1 {
		t.Fatalf("large-corpus ratio boundary score = %v, %v; want 1, nil", got, err)
	}
	large.CorpusDF[0] = 3
	if got, err := qualifiedContentScore(large, 0, 1, parameters); err != nil || got != 0 {
		t.Fatalf("large-corpus excessive single-term score = %v, %v; want 0, nil", got, err)
	}

	weak := evidenceIndex(
		[]string{"database", "protocol"},
		[]int{2, 2},
		[]Feature{{TermID: 0, Weight: 0.2, Fields: fieldBody}, {TermID: 1, Weight: 0.2, Fields: fieldBody}},
		[]Feature{{TermID: 0, Weight: 0.2, Fields: fieldBody}, {TermID: 1, Weight: 0.2, Fields: fieldBody}},
	)
	if got, err := qualifiedContentScore(weak, 0, 1, ContentParameters{MinCosine: 0.10, MaxSingleTermRatio: 0.10}); err != nil || got != 0 {
		t.Fatalf("below-minimum content score = %v, %v; want 0, nil", got, err)
	}

	excludedPairs := [][]string{
		{"2026-04-06", "2026-04-07"},
		{"v1.2", "version2.0"},
		{"550e8400-e29b-41d4-a716-446655440000", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"},
		{"deadbeefcafebabe", "0123456789abcdef"},
	}
	for _, terms := range excludedPairs {
		index := evidenceIndex(
			terms,
			[]int{2, 2},
			[]Feature{{TermID: 0, Weight: math.Sqrt(0.5), Fields: fieldTitle}, {TermID: 1, Weight: math.Sqrt(0.5), Fields: fieldTitle}},
			[]Feature{{TermID: 0, Weight: math.Sqrt(0.5), Fields: fieldTitle}, {TermID: 1, Weight: math.Sqrt(0.5), Fields: fieldTitle}},
		)
		if got, err := qualifiedContentScore(index, 0, 1, parameters); err != nil || got != 0 {
			t.Fatalf("excluded-term content score for %#v = %v, %v; want 0, nil", terms, got, err)
		}
	}
}

func TestIndependentStructuralRecall(t *testing.T) {
	features := evidenceIndex(nil, nil, nil, nil, nil)
	tags := &TagSignalIndex{DocumentTags: [][]int{{0}, {0}, {}}, Tags: []string{"go"}}
	graph := &model.LinkGraph{Forward: map[string][]string{
		"doc-0.md": {"doc-2.md"},
		"doc-1.md": {},
		"doc-2.md": {},
	}}
	parameters := ContentParameters{MinCosine: 0.05, MaxSingleTermRatio: 0.10}

	tagOnly, err := ScorePair(features, tags, graph, 0, 1, parameters)
	if err != nil || tagOnly.Content != 0 || tagOnly.Link != 0 || tagOnly.Tag != 0.15 || tagOnly.Final != 0.15 {
		t.Fatalf("tag-only score = %#v, %v; want independent 0.15 recall", tagOnly, err)
	}
	linkOnly, err := ScorePair(features, tags, graph, 0, 2, parameters)
	if err != nil || linkOnly.Content != 0 || linkOnly.Link != 0.20 || linkOnly.Tag != 0 || linkOnly.Final != 0.20 {
		t.Fatalf("link-only score = %#v, %v; want independent 0.20 recall", linkOnly, err)
	}

	weakFeatures := evidenceIndex(
		[]string{"database"},
		[]int{2},
		[]Feature{{TermID: 0, Weight: 1, Fields: fieldBody}},
		[]Feature{{TermID: 0, Weight: 1, Fields: fieldBody}},
	)
	weak, err := ScorePair(weakFeatures, nil, nil, 0, 1, parameters)
	if err != nil || weak.Final != 0 {
		t.Fatalf("weak-posting-only score = %#v, %v; want dropped zero signal", weak, err)
	}
}

func TestCombinedScore(t *testing.T) {
	features := evidenceIndex(
		[]string{"database", "protocol"},
		[]int{2, 2},
		[]Feature{{TermID: 0, Weight: 0.8, Fields: fieldBody}, {TermID: 1, Weight: 0.6, Fields: fieldBody}},
		[]Feature{{TermID: 0, Weight: 0.8, Fields: fieldBody}, {TermID: 1, Weight: 0.6, Fields: fieldBody}},
	)
	tags := &TagSignalIndex{DocumentTags: [][]int{{0}, {0}}, Tags: []string{"go"}}
	graph := &model.LinkGraph{Forward: map[string][]string{
		"doc-0.md": {"doc-1.md"},
		"doc-1.md": {"doc-0.md"},
	}}
	score, err := ScorePair(features, tags, graph, 0, 1, ContentParameters{MinCosine: 0.05, MaxSingleTermRatio: 0.10})
	if err != nil {
		t.Fatalf("ScorePair() error = %v", err)
	}
	if score.Content != 1 || score.Link != 0.50 || score.Tag != 0.15 || score.Final != 1.65 {
		t.Fatalf("ScorePair() = %#v, want fixed content/link/tag sum", score)
	}
}

func TestNeverRecommendsSelf(t *testing.T) {
	features := evidenceIndex(nil, nil, nil)
	score, err := ScorePair(features, nil, nil, 0, 0, ContentParameters{MinCosine: 0.05, MaxSingleTermRatio: 0.10})
	if err != nil || score != (PairScore{}) {
		t.Fatalf("ScorePair(self) = %#v, %v; want zero score", score, err)
	}
}

func evidenceIndex(terms []string, corpusDF []int, documentFeatures ...[]Feature) *FeatureIndex {
	index := &FeatureIndex{
		Terms:     append([]string(nil), terms...),
		CorpusDF:  append([]int(nil), corpusDF...),
		Documents: make([]FeatureDocument, len(documentFeatures)),
	}
	for docID, features := range documentFeatures {
		index.Documents[docID] = FeatureDocument{
			RelPath:  "doc-" + string(rune('0'+docID)) + ".md",
			Features: append([]Feature(nil), features...),
		}
	}
	return index
}

func evidenceIndexWithDocumentCount(documentCount int, terms []string, corpusDF []int) *FeatureIndex {
	features := make([][]Feature, documentCount)
	return evidenceIndex(terms, corpusDF, features...)
}
