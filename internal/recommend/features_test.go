package recommend

import (
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestFieldWeightedTF(t *testing.T) {
	counts := termFieldCounts{title: 2, aliases: 3, headings: 4, body: 5}
	want := 3.0*(1+math.Log(2)) +
		2.5*(1+math.Log(3)) +
		2.0*(1+math.Log(4)) +
		1.0*(1+math.Log(5))
	if got := weightedTermFrequency(counts); got != want {
		t.Fatalf("weightedTermFrequency() = %.17g, want %.17g", got, want)
	}
	if got := saturatedCount(0); got != 0 {
		t.Fatalf("saturatedCount(0) = %v, want 0", got)
	}

	documents := []model.RelatedSemanticDocument{
		{RelPath: "a.md", Title: "signal", Aliases: []string{"signal"}, Headings: []string{"signal"}, Body: "signal"},
		{RelPath: "b.md", Title: "signal", Aliases: []string{"signal"}, Headings: []string{"signal"}, Body: "signal"},
	}
	index, err := BuildFeatureIndex(documents, FeatureParameters{MaxFeatures: 32, MaxDFRatio: 0.40})
	if err != nil {
		t.Fatalf("BuildFeatureIndex() error = %v", err)
	}
	wantFields := fieldTitle | fieldAliases | fieldHeadings | fieldBody
	for _, document := range index.Documents {
		if len(document.Features) != 1 || document.Features[0].Fields != wantFields {
			t.Fatalf("%s features = %#v, want all field-presence bits", document.RelPath, document.Features)
		}
	}
}

func TestDocumentFrequencyAndIDF(t *testing.T) {
	previous := math.Inf(1)
	for documentFrequency := 1; documentFrequency <= 10; documentFrequency++ {
		got := inverseDocumentFrequency(10, documentFrequency)
		want := math.Log(1 + (float64(10-documentFrequency)+0.5)/(float64(documentFrequency)+0.5))
		if got != want {
			t.Fatalf("inverseDocumentFrequency(10, %d) = %.17g, want %.17g", documentFrequency, got, want)
		}
		if got >= previous {
			t.Fatalf("IDF at DF %d = %v, want strictly below previous %v", documentFrequency, got, previous)
		}
		previous = got
	}

	documents := make([]model.RelatedSemanticDocument, 20)
	for index := range documents {
		body := "shared"
		if index < 9 {
			body += " common"
		}
		if index < 8 {
			body += " edge"
		}
		if index < 2 {
			body += " rare rare rare"
		}
		documents[index] = model.RelatedSemanticDocument{RelPath: featureTestPath(index), Body: body}
	}
	featureIndex, err := BuildFeatureIndex(documents, FeatureParameters{MaxFeatures: 32, MaxDFRatio: 0.40})
	if err != nil {
		t.Fatalf("BuildFeatureIndex() error = %v", err)
	}
	if containsString(featureIndex.Terms, "shared") || containsString(featureIndex.Terms, "common") {
		t.Fatalf("terms = %#v, want ratios above 0.40 excluded", featureIndex.Terms)
	}
	if edgeID := stringIndex(featureIndex.Terms, "edge"); edgeID < 0 || featureIndex.CorpusDF[edgeID] != 8 {
		t.Fatalf("edge term ID/DF = %d/%v, want equality at ratio 0.40 retained", edgeID, featureIndex.CorpusDF)
	}
	termID := stringIndex(featureIndex.Terms, "rare")
	if termID < 0 || featureIndex.CorpusDF[termID] != 2 {
		t.Fatalf("rare term ID/DF = %d/%v, want repeated tokens to contribute DF 2", termID, featureIndex.CorpusDF)
	}

	small, err := BuildFeatureIndex([]model.RelatedSemanticDocument{
		{RelPath: "a.md", Body: "smallcommon"},
		{RelPath: "b.md", Body: "smallcommon"},
		{RelPath: "c.md", Body: "smallcommon"},
	}, FeatureParameters{MaxFeatures: 32, MaxDFRatio: 0.40})
	if err != nil {
		t.Fatalf("BuildFeatureIndex(small) error = %v", err)
	}
	if !containsString(small.Terms, "smallcommon") {
		t.Fatalf("small terms = %#v, want N<20 corpus to skip ratio cutoff", small.Terms)
	}
}

func TestFeatureSelection(t *testing.T) {
	terms := make([]string, 34)
	for index := range terms {
		terms[index] = featureTestTerm(index)
	}
	body := strings.Join(terms, " ")
	documents := []model.RelatedSemanticDocument{
		{RelPath: "zeta.md", Body: body},
		{RelPath: "alpha.md", Body: body},
	}

	featureIndex, err := BuildFeatureIndex(documents, FeatureParameters{MaxFeatures: 32, MaxDFRatio: 0.40})
	if err != nil {
		t.Fatalf("BuildFeatureIndex() error = %v", err)
	}
	if got := []string{featureIndex.Documents[0].RelPath, featureIndex.Documents[1].RelPath}; !reflect.DeepEqual(got, []string{"alpha.md", "zeta.md"}) {
		t.Fatalf("document order = %#v, want normalized RelPath order", got)
	}
	for docID, document := range featureIndex.Documents {
		if len(document.Features) != 32 {
			t.Fatalf("len(%s.Features) = %d, want 32", document.RelPath, len(document.Features))
		}
		gotTerms := featureTerms(featureIndex, document.Features)
		if !reflect.DeepEqual(gotTerms, terms[:32]) {
			t.Fatalf("%s selected terms = %#v, want lexical tie winners %#v", document.RelPath, gotTerms, terms[:32])
		}
		if !sort.SliceIsSorted(document.Features, func(left int, right int) bool {
			return document.Features[left].TermID < document.Features[right].TermID
		}) {
			t.Fatalf("%s features are not termID sorted", document.RelPath)
		}
		squaredNorm := 0.0
		for _, feature := range document.Features {
			if feature.Fields != fieldBody {
				t.Fatalf("%s feature fields = %04b, want body only", document.RelPath, feature.Fields)
			}
			squaredNorm += feature.Weight * feature.Weight
		}
		if math.Abs(squaredNorm-1) > 1e-12 {
			t.Fatalf("%s squared norm = %.17g, want 1", document.RelPath, squaredNorm)
		}
		for _, posting := range featureIndex.Postings[document.Features[0].TermID] {
			if posting.DocID < 0 || posting.DocID >= len(featureIndex.Documents) {
				t.Fatalf("posting docID = %d, want bounded document ID", posting.DocID)
			}
		}
		if got := featureIndex.Postings[document.Features[0].TermID][docID].DocID; got != docID {
			t.Fatalf("posting order at document %d = %d, want docID order", docID, got)
		}
	}

	weightedTerms := make([]string, 70)
	var weightedBody strings.Builder
	for index := range weightedTerms {
		weightedTerms[index] = featureTestTerm(index)
		for repetition := 0; repetition <= index; repetition++ {
			weightedBody.WriteString(weightedTerms[index])
			weightedBody.WriteByte(' ')
		}
	}
	for _, maxFeatures := range []int{32, 48, 64} {
		for _, maxRatio := range []float64{0.40, 0.50, 0.60} {
			if err := validateFeatureParameters(FeatureParameters{MaxFeatures: maxFeatures, MaxDFRatio: maxRatio}); err != nil {
				t.Errorf("validateFeatureParameters(%d, %.2f) error = %v", maxFeatures, maxRatio, err)
			}
		}
		weighted, err := BuildFeatureIndex([]model.RelatedSemanticDocument{
			{RelPath: "weighted-a.md", Body: weightedBody.String()},
			{RelPath: "weighted-b.md", Body: weightedBody.String()},
		}, FeatureParameters{MaxFeatures: maxFeatures, MaxDFRatio: 0.40})
		if err != nil {
			t.Fatalf("BuildFeatureIndex(weighted M=%d) error = %v", maxFeatures, err)
		}
		got := featureTerms(weighted, weighted.Documents[0].Features)
		want := weightedTerms[len(weightedTerms)-maxFeatures:]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("weighted Top-%d = %#v, want highest-weight terms %#v", maxFeatures, got, want)
		}
	}
	for _, invalid := range []FeatureParameters{
		{MaxFeatures: 31, MaxDFRatio: 0.40},
		{MaxFeatures: 32, MaxDFRatio: 0.41},
	} {
		if err := validateFeatureParameters(invalid); err == nil {
			t.Errorf("validateFeatureParameters(%#v) error = nil, want rejection", invalid)
		}
	}
}

func TestSelectedFeaturePruning(t *testing.T) {
	highTerms := make([]string, 32)
	for index := range highTerms {
		highTerms[index] = featureTestTerm(index)
	}
	documents := []model.RelatedSemanticDocument{
		{RelPath: "one.md", Title: "solo shared"},
		{RelPath: "two.md", Title: strings.Join(highTerms, " "), Body: "solo"},
		{RelPath: "three.md", Body: strings.Join(highTerms, " ") + " shared"},
	}

	featureIndex, err := BuildFeatureIndex(documents, FeatureParameters{MaxFeatures: 32, MaxDFRatio: 0.40})
	if err != nil {
		t.Fatalf("BuildFeatureIndex() error = %v", err)
	}
	if stringIndex(featureIndex.Terms, "solo") >= 0 {
		t.Fatal("selectedDF=1 term solo retained a final term ID")
	}
	if cap(featureIndex.Terms) != len(featureIndex.Terms) || cap(featureIndex.CorpusDF) != len(featureIndex.CorpusDF) {
		t.Fatalf("final term state len/cap = %d/%d and %d/%d, want compact backing arrays", len(featureIndex.Terms), cap(featureIndex.Terms), len(featureIndex.CorpusDF), cap(featureIndex.CorpusDF))
	}
	for _, document := range featureIndex.Documents {
		if containsString(featureTerms(featureIndex, document.Features), "solo") {
			t.Fatalf("%s features retain selectedDF=1 term solo", document.RelPath)
		}
	}
	one := featureIndex.Documents[stringIndex(featureDocumentPaths(featureIndex), "one.md")]
	if got := featureTerms(featureIndex, one.Features); !reflect.DeepEqual(got, []string{"shared"}) {
		t.Fatalf("one.md features after pruning = %#v, want shared without refill", got)
	}

	empty, err := BuildFeatureIndex([]model.RelatedSemanticDocument{
		{RelPath: "empty-a.md", Body: "uniquealpha"},
		{RelPath: "empty-b.md", Body: "uniquebeta"},
	}, FeatureParameters{MaxFeatures: 32, MaxDFRatio: 0.40})
	if err != nil {
		t.Fatalf("BuildFeatureIndex(empty vectors) error = %v", err)
	}
	for _, document := range empty.Documents {
		if len(document.Features) != 0 {
			t.Fatalf("%s empty-vector features = %#v, want none", document.RelPath, document.Features)
		}
	}
	if len(empty.Terms) != 0 || len(empty.Postings) != 0 {
		t.Fatalf("empty-vector index terms/postings = %#v/%#v, want empty", empty.Terms, empty.Postings)
	}
}

func TestSparseCosine(t *testing.T) {
	left := []Feature{{TermID: 0, Weight: 0.6}, {TermID: 2, Weight: 0.8}}
	right := []Feature{{TermID: 0, Weight: 0.8}, {TermID: 1, Weight: 0.6}}
	got, err := SparseCosine(left, right)
	if err != nil {
		t.Fatalf("SparseCosine() error = %v", err)
	}
	if want := 0.48; got != want {
		t.Fatalf("SparseCosine() = %.17g, want %.17g", got, want)
	}
	mirrored, err := SparseCosine(right, left)
	if err != nil || mirrored != got {
		t.Fatalf("SparseCosine() mirrored = %.17g, %v; want %.17g, nil", mirrored, err, got)
	}

	clampedHigh, err := SparseCosine([]Feature{{TermID: 1, Weight: 1 + 5e-13}}, []Feature{{TermID: 1, Weight: 1}})
	if err != nil || clampedHigh != 1 {
		t.Fatalf("SparseCosine(high rounding) = %v, %v; want 1, nil", clampedHigh, err)
	}
	clampedLow, err := SparseCosine([]Feature{{TermID: 1, Weight: -5e-13}}, []Feature{{TermID: 1, Weight: 1}})
	if err != nil || clampedLow != 0 {
		t.Fatalf("SparseCosine(low rounding) = %v, %v; want 0, nil", clampedLow, err)
	}
	if _, err := SparseCosine([]Feature{{TermID: 1, Weight: 1 + 2e-12}}, []Feature{{TermID: 1, Weight: 1}}); err == nil {
		t.Fatal("SparseCosine(high overflow) error = nil, want tolerance error")
	}
	if _, err := SparseCosine([]Feature{{TermID: 1, Weight: -2e-12}}, []Feature{{TermID: 1, Weight: 1}}); err == nil {
		t.Fatal("SparseCosine(low overflow) error = nil, want tolerance error")
	}
	if endpoint, err := SparseCosine([]Feature{{TermID: 1, Weight: 1 + 1e-12}}, []Feature{{TermID: 1, Weight: 1}}); err != nil || endpoint != 1 {
		t.Fatalf("SparseCosine(high endpoint) = %v, %v; want 1, nil", endpoint, err)
	}
	if endpoint, err := SparseCosine([]Feature{{TermID: 1, Weight: -1e-12}}, []Feature{{TermID: 1, Weight: 1}}); err != nil || endpoint != 0 {
		t.Fatalf("SparseCosine(low endpoint) = %v, %v; want 0, nil", endpoint, err)
	}
	if _, err := SparseCosine([]Feature{{TermID: 1, Weight: math.NaN()}}, []Feature{{TermID: 1, Weight: 1}}); err == nil {
		t.Fatal("SparseCosine(NaN) error = nil, want non-finite error")
	}
}

func TestFeatureIndexDeterministicIDs(t *testing.T) {
	documents := []model.RelatedSemanticDocument{
		{RelPath: "Ｂeta.md", Title: "gamma alpha", Body: "beta"},
		{RelPath: "alpha.md", Title: "alpha beta", Body: "gamma"},
		{RelPath: "delta.md", Title: "beta gamma", Body: "alpha"},
	}
	parameters := FeatureParameters{MaxFeatures: 32, MaxDFRatio: 0.40}
	first, err := BuildFeatureIndex(documents, parameters)
	if err != nil {
		t.Fatalf("BuildFeatureIndex(first) error = %v", err)
	}
	reversed := append([]model.RelatedSemanticDocument(nil), documents...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	second, err := BuildFeatureIndex(reversed, parameters)
	if err != nil {
		t.Fatalf("BuildFeatureIndex(reversed) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("feature indexes differ by input order:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if got := featureDocumentPaths(first); !reflect.DeepEqual(got, []string{"Ｂeta.md", "alpha.md", "delta.md"}) {
		t.Fatalf("normalized document IDs = %#v, want NFKC sort order", got)
	}
	if !reflect.DeepEqual(first.Terms, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("term IDs = %#v, want normalized lexical order", first.Terms)
	}

	equivalentPaths, err := BuildFeatureIndex([]model.RelatedSemanticDocument{
		{RelPath: "A.md", Body: "sharedterm"},
		{RelPath: "Ａ.md", Body: "sharedterm"},
	}, parameters)
	if err != nil {
		t.Fatalf("BuildFeatureIndex(normalization-equivalent paths) error = %v", err)
	}
	if got := featureDocumentPaths(equivalentPaths); !reflect.DeepEqual(got, []string{"A.md", "Ａ.md"}) {
		t.Fatalf("normalization-equivalent document order = %#v, want raw path tie-break", got)
	}
}

func featureTestPath(index int) string {
	return "notes/" + featureTestTerm(index) + ".md"
}

func featureTestTerm(index int) string {
	return "term" + string(rune('a'+index/26)) + string(rune('a'+index%26))
}

func featureTerms(index *FeatureIndex, features []Feature) []string {
	terms := make([]string, len(features))
	for position, feature := range features {
		terms[position] = index.Terms[feature.TermID]
	}
	return terms
}

func featureDocumentPaths(index *FeatureIndex) []string {
	paths := make([]string, len(index.Documents))
	for docID, document := range index.Documents {
		paths[docID] = document.RelPath
	}
	return paths
}

func containsString(values []string, want string) bool {
	return stringIndex(values, want) >= 0
}

func stringIndex(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}
