package recommend

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestEmptyAndSingletonCorpus(t *testing.T) {
	for _, semantics := range [][]model.RelatedSemanticDocument{
		nil,
		{{RelPath: "single.md", Body: "数据库"}},
	} {
		result, err := BuildEngine(semantics, nil, nil, EngineParameters{})
		if err != nil {
			t.Fatalf("BuildEngine(N=%d) error = %v, want early empty result", len(semantics), err)
		}
		if result == nil || len(result.Documents) != 0 {
			t.Fatalf("BuildEngine(N=%d) = %#v, want empty result", len(semantics), result)
		}
	}
}

func TestIndependentCandidateUnion(t *testing.T) {
	semantics := []model.RelatedSemanticDocument{
		{RelPath: "a.md", Body: "database protocol"},
		{RelPath: "b.md", Body: "database protocol"},
		{RelPath: "c.md", Body: "unrelated"},
		{RelPath: "d.md", Body: "different"},
	}
	idx := testRecommendIndex(
		&model.Note{RelPath: "a.md", Tags: []string{"go"}},
		&model.Note{RelPath: "b.md"},
		&model.Note{RelPath: "c.md"},
		&model.Note{RelPath: "d.md", Tags: []string{"GO"}},
	)
	graph := &model.LinkGraph{
		Forward: map[string][]string{
			"a.md": {"c.md"},
			"b.md": {},
			"c.md": {},
			"d.md": {},
		},
		Backward: map[string][]string{
			"a.md": {},
			"b.md": {},
			"c.md": {"a.md"},
			"d.md": {},
		},
	}
	result, err := BuildEngine(semantics, idx, graph, testEngineParameters(1, 10))
	if err != nil {
		t.Fatalf("BuildEngine() error = %v", err)
	}

	a := engineDocumentByPath(result, "a.md")
	if a == nil {
		t.Fatal("BuildEngine() missing a.md")
	}
	if got, want := engineRelatedPaths(result, a.Related), []string{"b.md", "c.md", "d.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a.md candidate union = %#v, want content/link/tag candidates %#v", got, want)
	}
	if got, want := []float64{a.Related[0].Score, a.Related[1].Score, a.Related[2].Score}, []float64{1, 0.20, 0.15}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a.md candidate scores = %#v, want %#v", got, want)
	}
}

func TestTopKOrdering(t *testing.T) {
	features := &FeatureIndex{Documents: []FeatureDocument{
		{RelPath: "source.md", NormalizedRelPath: "source.md"},
		{RelPath: "b.md", NormalizedRelPath: "b.md"},
		{RelPath: "A.md", NormalizedRelPath: "A.md"},
		{RelPath: "Ａ.md", NormalizedRelPath: "A.md"},
		{RelPath: "d.md", NormalizedRelPath: "d.md"},
	}}
	idx := testRecommendIndex(
		&model.Note{RelPath: "source.md", Frontmatter: model.Frontmatter{Title: "Source"}},
		&model.Note{RelPath: "b.md", Frontmatter: model.Frontmatter{Title: "B"}},
		&model.Note{RelPath: "A.md", Frontmatter: model.Frontmatter{Title: "Beta"}},
		&model.Note{RelPath: "Ａ.md", Frontmatter: model.Frontmatter{Title: "Alpha"}},
		&model.Note{RelPath: "d.md", Frontmatter: model.Frontmatter{Title: "D"}},
	)
	graph := &model.LinkGraph{
		Forward: map[string][]string{
			"source.md": {"A.md", "b.md", "d.md", "Ａ.md"},
			"A.md":      {},
			"b.md":      {},
			"d.md":      {"source.md"},
			"Ａ.md":      {},
		},
		Backward: map[string][]string{
			"source.md": {"d.md"},
			"A.md":      {"source.md"},
			"b.md":      {"source.md"},
			"d.md":      {"source.md"},
			"Ａ.md":      {"source.md"},
		},
	}
	result, err := rankFeatureIndex(features, &TagSignalIndex{DocumentTags: make([][]int, 5)}, idx, graph, testEngineParameters(1, 3), nil)
	if err != nil {
		t.Fatalf("rankFeatureIndex() error = %v", err)
	}
	if got, want := engineRelatedPaths(result, result.Documents[0].Related), []string{"d.md", "Ａ.md", "A.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Top-K ordering = %#v, want score/path/title order %#v", got, want)
	}
	if len(result.Documents[0].Related) != 3 {
		t.Fatalf("Top-K size = %d, want capacity 3", len(result.Documents[0].Related))
	}
}

func TestWorkerCountDeterminism(t *testing.T) {
	semantics := make([]model.RelatedSemanticDocument, 12)
	notes := make([]*model.Note, 12)
	for docID := range semantics {
		relPath := fmt.Sprintf("notes/%02d.md", docID)
		semantics[docID] = model.RelatedSemanticDocument{RelPath: relPath, Body: "database protocol"}
		notes[docID] = &model.Note{RelPath: relPath, Frontmatter: model.Frontmatter{Title: fmt.Sprintf("Article %02d", docID)}}
	}
	idx := testRecommendIndex(notes...)
	graph := emptyEngineGraph(semantics)

	var want *EngineResult
	for _, workers := range []int{1, 2, 4, 8} {
		got, err := BuildEngine(semantics, idx, graph, testEngineParameters(workers, 5))
		if err != nil {
			t.Fatalf("BuildEngine(workers=%d) error = %v", workers, err)
		}
		if want == nil {
			want = got
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildEngine(workers=%d) differs from workers=1", workers)
		}
		for docID := range got.Documents {
			for rank := range got.Documents[docID].Related {
				gotBits := math.Float64bits(got.Documents[docID].Related[rank].Score)
				wantBits := math.Float64bits(want.Documents[docID].Related[rank].Score)
				if gotBits != wantBits {
					t.Fatalf("workers=%d score bits at doc/rank %d/%d = %#x, want %#x", workers, docID, rank, gotBits, wantBits)
				}
			}
		}
	}
}

func TestBuildErrorPropagation(t *testing.T) {
	features := &FeatureIndex{
		Documents: []FeatureDocument{
			{RelPath: "a.md", NormalizedRelPath: "a.md", Features: []Feature{{TermID: 0, Weight: 1 + 2e-12, Fields: fieldTitle}}},
			{RelPath: "b.md", NormalizedRelPath: "b.md", Features: []Feature{{TermID: 0, Weight: 1, Fields: fieldTitle}}},
		},
		Terms:    []string{"database"},
		CorpusDF: []int{2},
		Postings: [][]Posting{{{DocID: 0, Weight: 1 + 2e-12}, {DocID: 1, Weight: 1}}},
	}
	_, err := rankFeatureIndex(features, &TagSignalIndex{DocumentTags: make([][]int, 2)}, nil, &model.LinkGraph{}, testEngineParameters(2, 1), nil)
	if err == nil || !strings.Contains(err.Error(), "cosine similarity above one tolerance") {
		t.Fatalf("rankFeatureIndex() error = %v, want propagated cosine overflow", err)
	}
}

func testEngineParameters(workers int, count int) EngineParameters {
	return EngineParameters{
		Features:    FeatureParameters{MaxFeatures: 32, MaxDFRatio: 0.40},
		Content:     ContentParameters{MinCosine: 0.05, MaxSingleTermRatio: 0.10},
		Count:       count,
		WorkerCount: workers,
	}
}

func engineDocumentByPath(result *EngineResult, relPath string) *EngineDocument {
	if result == nil {
		return nil
	}
	for index := range result.Documents {
		if result.Documents[index].RelPath == relPath {
			return &result.Documents[index]
		}
	}
	return nil
}

func engineRelatedPaths(result *EngineResult, ranked []RankedDocument) []string {
	paths := make([]string, len(ranked))
	for index, candidate := range ranked {
		paths[index] = result.Documents[candidate.DocID].RelPath
	}
	return paths
}

func emptyEngineGraph(semantics []model.RelatedSemanticDocument) *model.LinkGraph {
	graph := &model.LinkGraph{Forward: map[string][]string{}, Backward: map[string][]string{}}
	paths := make([]string, len(semantics))
	for index, document := range semantics {
		paths[index] = document.RelPath
	}
	sort.Strings(paths)
	for _, relPath := range paths {
		graph.Forward[relPath] = []string{}
		graph.Backward[relPath] = []string{}
	}
	return graph
}
