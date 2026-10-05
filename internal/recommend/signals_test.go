package recommend

import (
	"math"
	"reflect"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestLinkSignal(t *testing.T) {
	graph := &model.LinkGraph{Forward: map[string][]string{
		"a.md": {"b.md"},
		"b.md": {},
		"c.md": {},
	}}
	if got := LinkSignal(graph, "a.md", "b.md"); got != 0.20 {
		t.Fatalf("LinkSignal(one-way) = %v, want 0.20", got)
	}
	if got := LinkSignal(graph, "b.md", "a.md"); got != 0.20 {
		t.Fatalf("LinkSignal(reverse perspective) = %v, want 0.20", got)
	}

	graph.Forward["b.md"] = []string{"a.md"}
	if got := LinkSignal(graph, "a.md", "b.md"); got != 0.50 {
		t.Fatalf("LinkSignal(mutual) = %v, want 0.50", got)
	}
	if got := LinkSignal(graph, "a.md", "c.md"); got != 0 {
		t.Fatalf("LinkSignal(unlinked) = %v, want 0", got)
	}
	if got := LinkSignal(graph, "a.md", "a.md"); got != 0 {
		t.Fatalf("LinkSignal(self) = %v, want 0", got)
	}
}

func TestTagSignal(t *testing.T) {
	documents := []FeatureDocument{
		{RelPath: "a.md"},
		{RelPath: "b.md"},
		{RelPath: "c.md"},
	}
	idx := testRecommendIndex(
		&model.Note{RelPath: "a.md", Tags: []string{"#Go", "go", "Static Site"}},
		&model.Note{RelPath: "b.md", Tags: []string{"GO", "Other"}},
		&model.Note{RelPath: "c.md"},
	)
	index := BuildTagSignalIndex(documents, idx)
	if want := []string{"go", "other", "static-site"}; !reflect.DeepEqual(index.Tags, want) {
		t.Fatalf("tag IDs = %#v, want normalized lexical tags %#v", index.Tags, want)
	}
	if got, want := index.DocumentTags, [][]int{{0, 2}, {0, 1}, {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("document tags = %#v, want normalized deduplicated IDs %#v", got, want)
	}
	if got, want := index.Postings, [][]int{{0, 1}, {1}, {0}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tag postings = %#v, want deterministic docID postings %#v", got, want)
	}
	if got, want := index.Score(0, 1), 0.05; math.Abs(got-want) > 1e-15 {
		t.Fatalf("tag score = %.17g, want Jaccard score %.17g", got, want)
	}
	if got := index.Score(2, 2); got != 0 || math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("empty tag score = %v, want finite zero", got)
	}
}

func TestTagSignalLargeCorpusCoverageCutoff(t *testing.T) {
	documents := make([]FeatureDocument, 20)
	notes := make([]*model.Note, 20)
	for docID := range documents {
		relPath := featureTestPath(docID)
		documents[docID] = FeatureDocument{RelPath: relPath}
		tags := []string{"unique-" + featureTestTerm(docID)}
		if docID < 11 {
			tags = append(tags, "too-common")
		}
		if docID < 10 {
			tags = append(tags, "edge-common")
		}
		notes[docID] = &model.Note{RelPath: relPath, Tags: tags}
	}
	index := BuildTagSignalIndex(documents, testRecommendIndex(notes...))
	if containsString(index.Tags, "too-common") {
		t.Fatalf("large-corpus tags = %#v, want >50%% tag excluded", index.Tags)
	}
	if !containsString(index.Tags, "edge-common") {
		t.Fatalf("large-corpus tags = %#v, want exactly 50%% tag retained", index.Tags)
	}

	edgeID := stringIndex(index.Tags, "edge-common")
	if edgeID < 0 || len(index.Postings[edgeID]) != 10 {
		t.Fatalf("edge-common posting = %#v, want 10 documents", index.Postings)
	}
}
